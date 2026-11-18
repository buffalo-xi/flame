package daemon

import (
	"github.com/gin-gonic/gin"

	"flame/tools/sandbox"
	"flame/tools/xlog"
)

// handleInteractive 长交互端点：一条连接 = 一次喂命令，进程归注册表、跨连接存活。
//
// 与 handleExec 的差别只有两处：
//  1. 进程来源是 interactiveRegistry（懒建/复用），而不是 exec.Start；
//  2. 客户端断开不杀进程 —— 只是「本次调用结束」，进程归还注册表。
//
// 协议（复用 exec 的帧）：
//
//	open(session_id + tool_id [+ argv/mounts 首次]) → ready
//	→ ChanStdin 喂命令 → 客户端断开 = 本次调用结束（进程保留）
//	→ FrameKill = 显式杀进程
//
// stdout 转发与进程退出检测都不在本 handler 里 —— 它们在 spawn 的常驻 goroutine
// 里（stdout 广播 + Wait 收尸）。这里只剩一根 stdin 要喂，因此没有 io.Copy、
// 也没有 copyDone/clientGone 那套一次性转发逻辑。
func (s *Server) handleInteractive(c *gin.Context) {
	ws, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := sandbox.NewConn(ws)
	defer func() { _ = ws.Close() }()

	// 1) open：session_id + tool_id，首次调用还带 argv + mounts
	f, _, rerr := wc.Recv()
	if rerr != nil {
		return
	}
	if f.Type != sandbox.FrameOpen {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "expected open frame"})
		return
	}
	if f.SessionID == "" || f.ToolID == "" {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "session_id and tool_id are required"})
		return
	}

	var argv []string
	var mounts []sandbox.Mount
	var dir string
	var reqLimits sandbox.Limits
	if f.Spec != nil {
		spec := f.Spec.Spec()
		argv = spec.Argv
		mounts = spec.Mounts
		dir = spec.Dir
		reqLimits = spec.Limits
	}

	if gerr := guardMounts(mounts, s.cfg.AllowedMountRoots); gerr != nil {
		xlog.Error("sandboxd: interactive rejected mounts: %v", gerr)
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: gerr.Error()})
		return
	}

	lim := s.cfg.ExecLimits(reqLimits)

	// 浏览器工具与 shell 共用这条通道（都是「session 级常驻进程」），
	// 差别只在 argv 由本服务组装：沙箱内 browserhost 叫什么、profile 放哪，
	// 是 sandboxd 才知道的事实（与 langs / codehost 同一个理由）。
	// 资源上限也换成 browser.limits —— Chrome 的进程树远大于一条 shell。
	if f.ToolID == BrowserToolID {
		if !s.cfg.Browser.Enabled {
			_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError,
				Message: "browser disabled: set browser.enabled: true in sandboxd config"})
			return
		}
		bargs, berr := s.cfg.BrowserArgv(s.cfg.Mode)
		if berr != nil {
			_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: berr.Error()})
			return
		}
		argv = bargs
		lim = s.cfg.BrowserLimits(reqLimits)
	}

	// 2) 懒建/复用进程（进程被锁住，本次调用独占）
	// f.Ephemeral：无 session 的临时进程，注册表用短空闲超时兜底回收。
	tp, aerr := s.interactive.acquire(f.SessionID, mounts, f.ToolID, argv, lim,
		acquireOpts{dir: dir, ephemeral: f.Ephemeral})
	if aerr != nil {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: aerr.Error()})
		return
	}
	defer tp.unlock() // 本次调用结束释放锁；不杀进程

	// stdout/stderr 都跟随当前连接；调用结束复位为丢弃（避免写到已关闭连接）
	tp.stderr.set(&wsWriter{c: wc, ch: sandbox.ChanStderr})
	tp.stdoutOut.set(&wsWriter{c: wc, ch: sandbox.ChanStdout})
	defer tp.stderr.set(nil)
	defer tp.stdoutOut.set(nil)

	if werr := wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameReady}); werr != nil {
		return
	}

	// 3) 客户端 → 进程 stdin。
	//
	// 断开 = 本次调用结束（进程保留）；FrameKill = 显式杀并从注册表移除。
	for {
		cf, data, rerr := wc.Recv()
		if rerr != nil {
			return // 客户端断开，进程保留
		}
		if data != nil {
			if data[0] == sandbox.ChanStdin {
				if _, werr := tp.stdin.Write(data[1:]); werr != nil {
					return
				}
			}
			continue
		}
		switch cf.Type {
		case sandbox.FrameKill:
			// 先杀，再看还剩不剩：只有这个 session 的工具全没了才通知「已空」——
			// 网关据此解掉粘性绑定。多工具会话杀一个不算作废。
			//
			// 帧要在连接关闭前发出（defer 才关 ws）；对端不认识这个帧时会忽略，
			// 所以老客户端不受影响。
			if empty := s.interactive.removeTool(f.SessionID, f.ToolID); empty {
				_ = wc.SendFrame(&sandbox.Frame{
					Type:      sandbox.FrameSessionEmpty,
					SessionID: f.SessionID,
				})
			}
			return
		}
	}
}
