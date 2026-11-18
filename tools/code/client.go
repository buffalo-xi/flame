// Package code 提供「代码片段会话」客户端：把双工执行委托给远端沙箱服务 sandboxd
// 的 /v1/code 通道（2026-10 由 /v1/script 更名）。
//
// 与旧设计的最大差异：本侧不再持有 worker 池。
//   - 池是否存在、隔离方式用哪种，全部由 sandboxd 决定；
//   - 每次 Execute 建立一条到 sandboxd 的会话连接，结束后关闭；
//   - 片段是纯「输入 → 返回」，不再有回调交互。
package code

import (
	"context"
	"fmt"
	"time"

	"flame/tools/sandbox"
)

// 错误码定义
//
// ⚠️ 其中 timeout / busy / unsupported_lang 是**线上协议值**：由 sandboxd 在 error 帧里
// 原样回传（见 sandboxd/internal/daemon/handle_code.go 的同名常量），本包只是接收端。
// 改动必须两端同步 —— 改单边只会让节点拿到一个谁也认不出的码。
const (
	ErrCodeUnsupportedLang = "unsupported_lang" // 不支持的语言
	ErrCodeConnectFail     = "connect_fail"     // 连接 sandboxd 失败
	ErrCodeSendFail        = "send_fail"        // 发送请求失败
	ErrCodeTimeout         = "timeout"          // 执行超时
	ErrCodeBusy            = "busy"             // 沙箱繁忙：并发额度耗尽，取槽等待超时被拒（脚本未执行）
	ErrCodeWorkerComm      = "worker_comm"      // worker 通信错误
	ErrCodeScriptError     = "script_error"     // 脚本执行错误
	ErrCodeUnknownAction   = "unknown_action"   // 未知 action
	ErrCodeCancel          = "cancel"           // 上下文取消/超时
)

// defaultTimeout 调用方未指定超时时的兜底。
const defaultTimeout = 30 * time.Second

// Error /v1/code 通道返回的错误，包含错误码和原始错误。
//
// 名字不再叫 ScriptError：它标识的是**通道**（code）而不是被执行的片段，
// 片段自身出错只是其中一种 Code（ErrCodeScriptError）。
type Error struct {
	Code    string // 错误码，如上 ErrCodeXxx 常量
	Message string // 错误信息
	Logs    string // 错误时的完整 stderr（含 traceback / stack）
	Err     error  // 原始错误（可选，用于 errors.Unwrap）
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Err }

// NewError 创建 Error
func NewError(code, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// Result 一次脚本执行的产出：脚本返回值 + print 输出（logs）。
type Result struct {
	Value any    // 脚本返回值（原 result）
	Logs  string // print 输出全文（可能被截断）
}

// Client 脚本会话客户端。
//
// 无状态、可并发复用：每次 Execute 独立建立一条会话连接，
// 因此嵌套调用（如 binding transform 内再执行脚本）不会自我阻塞。
type Client struct {
	endpoint string         // 形如 ws://sandboxd:8080/v1/code
	token    string         // 可选 Bearer 令牌
	limits   sandbox.Limits // 默认资源上限（除 timeout 外一般由 sandboxd 决定）

	// onReady 可选的交互观测钩子：sandboxd 回 ready 后触发一次，
	// 带上它分配的 script ID（沙箱侧的 worker ID）与是否复用了池中对象。
	//
	// 存在的意义：容器与解释器的身份只存在于 sandboxd 侧，本侧能拿到的唯一线索
	// 就是这个 script ID —— 不接住它，两边的日志就无从对账（sandboxd 记 container，
	// 本侧记业务上下文，靠 script 串起来）。
	// 约定：启动时设置一次、之后只读，因此不加锁；并发 Execute 期间不要再改。
	onReady func(scriptID string, reused bool)
}

// NewClient 创建脚本会话客户端。
func NewClient(endpoint, token string, limits sandbox.Limits) *Client {
	return &Client{endpoint: endpoint, token: token, limits: limits}
}

// SetOnReady 设置「交互就绪」观测钩子（可选，默认不记录）。
// 必须在任何 Execute 之前调用一次（并发约定见 Client.onReady）。
func (c *Client) SetOnReady(fn func(scriptID string, reused bool)) { c.onReady = fn }

// Close 保留以兼容旧的 defer 调用（客户端无池，无需释放）。
func (c *Client) Close() {}

// Execute 执行一次脚本。
//
//	ctx     - 上下文，用于取消
//	lang    - 语言："lua" / "python" / "js"
//	script  - 脚本内容
//	inputs  - 输入变量
//	timeout - 超时时间（秒），<=0 使用 defaultTimeout
func (c *Client) Execute(ctx context.Context, lang, script string, inputs map[string]any,
	timeout int) (*Result, error) {

	execTimeout := time.Duration(timeout) * time.Second
	if execTimeout <= 0 {
		execTimeout = defaultTimeout
	}

	conn, err := sandbox.Dial(ctx, c.endpoint, c.token)
	if err != nil {
		return nil, NewError(ErrCodeConnectFail,
			fmt.Sprintf("connect sandboxd: %v", err), err)
	}
	defer func() { _ = conn.Close() }()

	// 资源上限：只带 timeout（其余由 sandboxd 的用途默认值决定并 clamp）
	lim := c.limits
	lim.Timeout = execTimeout

	// 1) open：双工只传 lang + limits —— worker 脚本路径与挂载由 sandboxd 自决
	if err := conn.SendFrame(&sandbox.Frame{
		Type: sandbox.FrameOpen,
		Lang: lang,
		Spec: &sandbox.WireSpec{Limits: lim.Wire()},
	}); err != nil {
		return nil, NewError(ErrCodeSendFail, fmt.Sprintf("send open: %v", err), err)
	}

	// 2) 等 ready
	rf, _, rerr := conn.Recv()
	if rerr != nil {
		return nil, NewError(ErrCodeWorkerComm, fmt.Sprintf("wait ready: %v", rerr), rerr)
	}
	switch rf.Type {
	case sandbox.FrameReady:
		// 接住 script ID：这是与 sandboxd 日志对账的唯一键 ——
		// 它知道这次调用落在哪个容器，本侧不知道，但它把这个 ID 回传了。
		if c.onReady != nil {
			c.onReady(rf.ScriptID, rf.Reused)
		}
	case sandbox.FrameError:
		return nil, NewError(orCode(rf.ErrCode), rf.Message, nil)
	default:
		return nil, NewError(ErrCodeWorkerComm,
			fmt.Sprintf("unexpected frame %q (want ready)", rf.Type), nil)
	}

	// 3) exec
	if err := conn.SendFrame(&sandbox.Frame{
		Type:   sandbox.FrameExec,
		Script: script,
		Inputs: inputs,
	}); err != nil {
		return nil, NewError(ErrCodeSendFail, fmt.Sprintf("send execute: %v", err), err)
	}

	// 4) 结果循环（等待 done / error / exit）
	deadline := time.NewTimer(execTimeout)
	defer deadline.Stop()

	type recvResult struct {
		f    *sandbox.Frame
		data []byte
		err  error
	}
	recvCh := make(chan recvResult, 1)

	abort := func(code, msg string, cause error) (*Result, error) {
		// 主动 kill：远端会销毁 worker（不回池），避免半截状态被复用
		_ = conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameKill})
		return nil, NewError(code, msg, cause)
	}

	for {
		go func() {
			f, data, err := conn.Recv()
			recvCh <- recvResult{f: f, data: data, err: err}
		}()

		var r recvResult
		select {
		case r = <-recvCh:
		case <-deadline.C:
			return abort(ErrCodeTimeout,
				fmt.Sprintf("execution timeout after %v", execTimeout), nil)
		case <-ctx.Done():
			return abort(ErrCodeCancel,
				fmt.Sprintf("execution canceled: %v", ctx.Err()), ctx.Err())
		}

		if r.err != nil {
			return nil, NewError(ErrCodeWorkerComm,
				fmt.Sprintf("worker communication: %v", r.err), r.err)
		}
		if r.f == nil {
			continue // 双工不使用二进制帧
		}

		switch r.f.Type {
		case sandbox.FrameDone:
			// 正常结束 → 告知沙箱服务可回收该 worker
			_ = conn.SendFrame(&sandbox.Frame{Type: sandbox.FrameClose})
			return &Result{Value: r.f.Result, Logs: r.f.Logs}, nil

		case sandbox.FrameError:
			se := NewError(orCode(r.f.ErrCode), r.f.Message, nil)
			se.Logs = r.f.Logs
			return nil, se

		case sandbox.FrameExit:
			if r.f.Code != 0 {
				return nil, NewError(ErrCodeScriptError,
					fmt.Sprintf("worker exited with code %d", r.f.Code), nil)
			}
			return nil, NewError(ErrCodeWorkerComm, "worker exited unexpectedly", nil)

		default:
			// 未知帧：忽略（前向兼容）
		}
	}
}

// orCode 空错误码时回退到通用脚本错误。
func orCode(code string) string {
	if code == "" {
		return ErrCodeScriptError
	}
	return code
}
