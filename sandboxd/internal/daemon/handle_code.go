package daemon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// closeGrace 收到 close 后等待 worker 收尾的最长时间。
const closeGrace = 5 * time.Second

// errCodeTimeout 会话超时的错误码，与 nexus 侧 tools/code 的 ErrCodeTimeout（"timeout"）
// 对齐。刻意不 import 那个客户端包（依赖方向相反）：这是两端之间的线上协议值，
// 与 unsupported_lang 同一性质，改动必须同步 tools/code/client.go。
//
// 为什么要给它一个专用码：nexus 的 code 执行器按码分派（service/flow/executors/code.go:81），
// 空码会被 orCode 归一成 script_error，于是「超时」在节点上显示成「脚本错误」，
// on_error 规则也没法按超时分类处理。
const errCodeTimeout = "timeout"

// errCodeBusy 会话被拒（并发额度耗尽、取槽等待超时）的错误码，与 nexus 侧
// tools/code 的 ErrCodeBusy（"busy"）对齐，同上：线上协议值，改动必须同步。
//
// 为什么要给它一个专用码：它是一次**可重试的瞬时拒绝**（脚本压根没执行），
// 混进 other / script 之后，工作流既没法按码配 on_error.retry，
// 日志里也看不出「池满了」还是「脚本炸了」。
const errCodeBusy = "busy"

// codeDeadline 一次交互的总时长上限。
//
// ⚠️ 语义是「一次交互最长活多久」，**不是**「空闲多久才回收」：计时从 open 帧起算、
// 全程不复位，覆盖 exec 与全部回调。配置键是 code_max_sec（旧名 session_idle_sec
// 仍收，见 config.go 的 applyLegacyKeys），语义以本函数为准。
//
// 不变量：调用方下发的超时**只能收紧**它，不能放宽 —— nexus 侧 script.<lang>.timeout
// 配得比这里大等于没配（这正是踩过的坑：配了 600 以为脚本能跑 10 分钟，实际 120s 就断）。
func codeDeadline(reqTimeout time.Duration, maxSec int) time.Duration {
	hardCap := time.Duration(maxSec) * time.Second
	if reqTimeout > 0 && reqTimeout < hardCap {
		return reqTimeout
	}
	return hardCap
}

// handleCode 双工端点：脚本 + 回调。
//
// 约定：一条连接只执行一次脚本（nexus 每次 Execute 建一条连接）。
// 这正好让「一次 execute 已完成」成为可判定的干净状态，从而安全归还池。
func (s *Server) handleCode(c *gin.Context) {
	ws, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := sandbox.NewConn(ws)
	defer func() { _ = ws.Close() }()

	// 1) open：双工只带 lang（+ 可选 limits）——脚本路径与挂载由本服务自决
	f, _, rerr := wc.Recv()
	if rerr != nil {
		return
	}
	if f.Type != sandbox.FrameOpen || f.Lang == "" {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "expected open frame with lang"})
		return
	}

	pool, ok := s.pools[f.Lang]
	if !ok {
		_ = wc.SendFrame(&sandbox.Frame{
			Type:    sandbox.FrameError,
			ErrCode: "unsupported_lang",
			Message: "unsupported language: " + f.Lang,
		})
		return
	}

	var reqLimits sandbox.Limits
	if f.Spec != nil {
		reqLimits = f.Spec.Limits.Limits()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 交互总时长上限（同时也是 codehost 单次 execute 的超时预算）：一条连接只执行
	// 一次脚本，两者语义重合。提前算好，execute 时随请求下发给 codehost ——
	// 让死循环的脚本在解释器层被 kill，而不是等整条连接挂到 deadline。
	deadline := codeDeadline(reqLimits.Timeout, s.cfg.CodeMaxSec)

	// 2) 借槽位（并发闸 + 池复用，超时报「繁忙」而不是无限等）
	w, reused, aerr := pool.Acquire(ctx)
	if aerr != nil {
		_ = wc.SendFrame(&sandbox.Frame{
			Type: sandbox.FrameError, ErrCode: errCodeBusy, Message: aerr.Error(),
		})
		return
	}

	var reusable atomic.Bool // 会话正常结束且 worker 干净时才置 true
	var clean atomic.Bool    // worker 处于「一次 execute 已完成」状态

	// 交互明细：解释器会话身份只存在于本服务 —— nexus 只知道语言名，拿不到「这次
	// 调用落在哪个 codehost 会话」。script(w.ID) 就是两侧对账的 join key
	// （通过 ready 帧回传，见 tools/code 的 Client.onReady）。
	//
	// 只在收尾记一条，且是否记录由 cfg.CodeLog 决定：lua 是 µs 级高频调用，
	// 每次首尾各记一条会把日志写成噪声（见 config.go 的 ScriptLogXxx）。
	started := time.Now()
	defer func() {
		// 先固定耗时再归还：归还路径可能包含杀进程等待，不该算进调用耗时。
		dur := time.Since(started).Round(time.Millisecond)
		pool.Release(w, reusable.Load())
		// 不可归还 = 这次会话没走干净（中断 / 超时 / 协议错误），强制记录；
		// 其余才交给 code_log 策略过滤（off / slow / all）。
		if !reusable.Load() || s.cfg.CodeLogEnabled(dur) {
			xlog.Info("sandboxd: code %s done lang=%s reused=%v dur=%s reusable=%v",
				w.ID, f.Lang, reused, dur, reusable.Load())
		}
	}()

	if werr := wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameReady, ScriptID: w.ID, Reused: reused}); werr != nil {
		xlog.Info("sandboxd: code %s client gone before ready", w.ID)
		return
	}

	workerDone := make(chan struct{})
	clientDone := make(chan struct{})
	var workerOnce sync.Once
	stopWorker := func() { workerOnce.Do(func() { close(workerDone) }) }

	// 3a) worker → client（本次 execute 结束即返回）
	go func() {
		defer stopWorker()
		for {
			msg, rerr := w.Recv()
			if rerr != nil {
				return
			}
			action, _ := msg["action"].(string)
			switch action {
			case "pong":
				continue

			case "done":
				clean.Store(true)
				_ = wc.SendFrame(&sandbox.Frame{
					Type:   sandbox.FrameDone,
					Result: msg["result"],
					Logs:   strOf(msg["logs"]),
				})
				return

			case "error":
				m, _ := msg["error"].(string)
				logs := strOf(msg["logs"])
				// 脚本错误必须落日志：收尾只记 "script done"（不含执行结果），
				// 不在这里记的话，脚本炸了日志里只有一条 done，看不出任何失败痕迹。
				// stderr 带 traceback，截断后一并记录，避免超长输出刷屏。
				if logs != "" {
					xlog.Error("sandboxd: code %s error lang=%s: %s\n%s",
						w.ID, f.Lang, m, truncateLog(logs, 4096))
				} else {
					xlog.Error("sandboxd: code %s error lang=%s: %s", w.ID, f.Lang, m)
				}
				_ = wc.SendFrame(&sandbox.Frame{
					Type:    sandbox.FrameError,
					Message: m,
					Logs:    logs,
				})
				return

			default:
				_ = wc.SendFrame(&sandbox.Frame{
					Type: sandbox.FrameError, Message: "unknown worker action: " + action,
				})
				return
			}
		}
	}()

	// 3b) client → worker（协议翻译：WS 控制帧 ↔ worker stdio 帧）
	go func() {
		defer close(clientDone)
		execSent := false
		for {
			cf, data, rerr := wc.Recv()
			if rerr != nil {
				return // 客户端断开：worker 侧由下方收尾逻辑处理
			}
			if data != nil {
				continue // 双工不使用二进制帧
			}

			switch cf.Type {
			case sandbox.FrameExec:
				if execSent {
					_ = wc.SendFrame(&sandbox.Frame{
						Type: sandbox.FrameError, Message: "one execute per script",
					})
					return
				}
				execSent = true
				clean.Store(false)
				// lang / command / timeout_ms 是 codehost（L3）要的：它据此 fork 解释器并
				// 施加 per-execute 超时。L2 的 worker 脚本不认这些字段，忽略之，照常用
				// script / inputs / callbacks。两端共用同一条 execute 帧，各取所需。
				if serr := w.Send(map[string]any{
					"action":     "execute",
					"lang":       pool.lang,
					"command":    pool.cfg.Command,
					"script":     cf.Script,
					"inputs":     cf.Inputs,
					"timeout_ms": deadline.Milliseconds(),
				}); serr != nil {
					return
				}

			case sandbox.FrameKill:
				w.Kill()
				return

			case sandbox.FrameClose:
				// 正常结束：等 worker 收尾（此时应已 done）
				select {
				case <-workerDone:
					reusable.Store(clean.Load())
				case <-time.After(closeGrace):
					xlog.Error("sandboxd: code %s close but worker not idle, discard", w.ID)
				}
				return
			}
		}
	}()

	// 4) 交互总时长上限：请求超时只能收紧它（见 codeDeadline），防连接挂死长期占用 worker。
	select {
	case <-clientDone:
	case <-time.After(deadline):
		// 文案带上限值与来源：这条错误会一路传到工作流节点，含糊的 "session idle timeout"
		// 会让人以为是「闲置回收」，而实际是「脚本跑了太久」，且看不出上限是多少。
		_ = wc.SendFrame(&sandbox.Frame{
			Type:    sandbox.FrameError,
			ErrCode: errCodeTimeout,
			Message: fmt.Sprintf(
				"script execution timeout after %v (script cap: code_max_sec=%ds, a request timeout can only lower it)",
				deadline, s.cfg.CodeMaxSec),
		})
	}

	// 5) 确保 worker 侧收尾（还活着就强杀 —— 状态不明，不会归还池）
	select {
	case <-workerDone:
	default:
		w.Kill()
		<-workerDone
	}
}

// strOf 把 worker 帧里的可选字符串字段安全取出（缺省 / 非 string 均得空串）。
// 用于 logs 这种「可能缺席」的字段，避免 .(string) 断言到 nil 时 panic。
func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// truncateLog 截断日志文本到 max 字节，超长在末尾标注总字节数。用于把脚本 stderr
// （traceback / print 可能很长）落日志时不刷屏。
func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... (truncated, %d bytes total)", len(s))
}
