package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"

	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// handleExec 单工端点：一条连接 = 一个进程，跑到退出为止。
//
// ⚠️ 「单工」在这里指**不池化**（每次新建、跑完即弃、无回调通道），
// 不是「单向」也不是「短命」：线上协议是完整双向流 —— stdin 可续写、可半关闭，
// stdout / stderr 分流回传，还支持 kill 帧。同时它**没有**双工那样的内建硬超时
// （对比 codeDeadline）：超时只来自 limits.timeout_sec（用途默认 → 请求覆盖 →
// 全局 clamp，0 = 不限时）。长驻的交互式命令直接走这条路径即可，不必新增端点。
//
// 为什么不入池：池化它需要「重置进程状态」，成本比直接启动还高，且没有复用收益。
func (s *Server) handleExec(c *gin.Context) {
	ws, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	wc := sandbox.NewConn(ws)
	defer func() { _ = ws.Close() }()

	// /metrics 负载计数：一次 exec 连接 = 一个 in-flight。
	s.execInFlight.Add(1)
	defer s.execInFlight.Add(-1)

	// 1) open（单工：携带完整 Spec）
	f, _, rerr := wc.Recv()
	if rerr != nil {
		return
	}
	if f.Type != sandbox.FrameOpen || f.Spec == nil {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: "expected open frame with spec"})
		return
	}

	spec := f.Spec.Spec()

	// 解析启动意图：语言路径（解释器由本服务按当前 mode 解析）或直传路径（调用方给全 argv）。
	// 失败在起进程之前就返回，避免把错误拼出来的命令交给隔离层。
	argv, aerr := buildExecArgv(f.Lang, spec.Argv, spec.Script, spec.Args, s.cfg.ResolveLangCommand)
	if aerr != nil {
		code := "bad_request"
		if errors.Is(aerr, ErrUnsupportedLang) {
			code = "unsupported_lang"
		}
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, ErrCode: code, Message: aerr.Error()})
		return
	}
	spec.Argv = argv
	if gerr := guardMounts(spec.Mounts, s.cfg.AllowedMountRoots); gerr != nil {
		xlog.Error("sandboxd: rejected mounts: %v", gerr)
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: gerr.Error()})
		return
	}

	// Dir 同样必须落在某个挂载点之下。
	//
	// none 没有 rootfs/镜像作为边界：TranslateToHost 匹配不到挂载点时会原样
	// 返回输入，于是宿主绝对路径会被直接当成工作目录。nsjail/docker 下
	// rootfs / 镜像本身就是边界，容器内的 /tmp 之类是合法的，因此只对 none 强制。
	if s.cfg.Mode == isolate.NoneName {
		if derr := guardDir(spec.Dir, spec.Mounts); derr != nil {
			xlog.Error("sandboxd: rejected dir: %v", derr)
			_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: derr.Error()})
			return
		}
	}

	// 网络裁决必须先于起进程：裁决不过就直接回错误帧，不要落到 isolate 再失败
	// （那时容器/进程已经建了一半，错误也更难解释）。
	if nerr := s.resolveExecNetwork(spec); nerr != nil {
		xlog.Error("sandboxd: rejected egress: %v", nerr)
		_ = wc.SendFrame(&sandbox.Frame{
			Type: sandbox.FrameError, ErrCode: "egress_denied", Message: nerr.Error(),
		})
		return
	}

	spec.Limits = s.cfg.ExecLimits(spec.Limits)
	// stderr 走独立通道回传，而不是写到 sandboxd 的 stderr
	spec.Stderr = &wsWriter{c: wc, ch: sandbox.ChanStderr}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proc, perr := s.exec.Start(ctx, spec)
	if perr != nil {
		_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameError, Message: perr.Error()})
		return
	}
	if werr := wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameReady}); werr != nil {
		_ = proc.Kill()
		return
	}

	// 2) 客户端 → 进程 stdin（含半关闭与强杀）
	//
	// clientGone 是「这条连接已经没人要了」的信号：读循环是唯一能察觉客户端
	// 断开的地方，所以由它关闭这个 channel 来通知下面的转发收尾。
	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		for {
			cf, data, rerr := wc.Recv()
			if rerr != nil {
				_ = proc.Kill() // 客户端断开：连带杀掉整棵进程树（docker 下是容器）
				return
			}
			if data != nil {
				if data[0] == sandbox.ChanStdin {
					if _, werr := proc.Stdin().Write(data[1:]); werr != nil {
						return
					}
				}
				continue
			}
			switch cf.Type {
			case sandbox.FrameStdinEOF:
				_ = proc.Stdin().Close()
			case sandbox.FrameKill:
				_ = proc.Kill()
				return
			}
		}
	}()

	// 3) 进程 stdout → 客户端
	//
	// 转发必须放在独立 goroutine 里，让主 goroutine 保持「可中断的等待」——
	// 否则客户端一断开、而管道又被攥住时，这次转发会永久卡住（见下面的说明）。
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(&wsWriter{c: wc, ch: sandbox.ChanStdout}, proc.Stdout())
	}()

	// 4) 等这次转发收尾。
	//
	// 正常路径是 stdout EOF —— 但「stdout EOF」并不等于「进程退出」：stdout 是被
	// 整棵进程树继承的 fd，只要还有一个孙进程（或容器里的残留进程）攥着它，
	// EOF 就永远不来。
	//
	// 客户端此时若已断开，这段输出已经没有去处：先杀干净，再强制释放管道把上面的
	// io.Copy 解开。否则这个 handler 会带着一个 goroutine 和一个 fd 永久卡住 ——
	// 「进程杀了但连接收不了尾」同样是没杀干净。
	select {
	case <-copyDone:
	case <-clientGone:
		_ = proc.Kill()
		_ = proc.Stdout().Close()
		<-copyDone
	}

	werr := proc.Wait()
	_ = wc.SendFrame(&sandbox.Frame{Type: sandbox.FrameExit, Code: exitCode(werr)})
}

// resolveExecNetwork 把请求的网络意图按部署上限裁决，回写成明确的布尔。
//
// 这是「部署上限 × 请求意图」这一层；租户许可在 nexus 侧已经算过 —— 它没许可就不会
// 传 egress=true。裁决结果写回 spec.Egress，isolate 只照着执行，不再判断策略。
//
// nil 为什么保留成 nil：nil 表示「调用方没表态」（老 nexus、或非 exec 通道），按部署
// 默认走；显式 false 才表示「这次一定要断网」。二者在 on-request 下都断网，但在
// mode=none 下待遇不同 —— 见下面的分支。
func (s *Server) resolveExecNetwork(spec *sandbox.Spec) error {
	want := spec.Egress != nil && *spec.Egress

	switch s.cfg.EffectiveNetwork() {
	case NetworkOff:
		if want {
			return errors.New("network=off: 本部署不提供出网，请求里的 egress 无法满足")
		}
		// 清成 nil：这一档的事实由配置决定（none / --iface_no_lo），
		// 不让请求里的 false 参与 —— 两者结论相同，但只有一个来源。
		spec.Egress = nil

	case NetworkOn:
		// 一律出网：忽略请求的 egress —— 这一档的控制权在部署方，不在调用方。
		//
		// 必须显式清成 nil：留着 false 会让 isolate 起成 --network none，
		// 「on 忽略请求」就变成了「on 听请求」，语义正好相反。
		spec.Egress = nil

	case NetworkOnRequest:
		if !want && s.cfg.Mode == isolate.NoneName {
			// 宿主进程与外部网络同源，断不了。请求明确要断网时只能拒绝：
			// 静默按出网跑就是撒谎（agent 会以为数据没出机器）。
			return errors.New("mode=none 无法提供断网执行（宿主进程拦不住网络）；" +
				"要么改用 docker / nsjail，要么把 network 声明为 on")
		}
		egress := want
		spec.Egress = &egress

	default:
		return fmt.Errorf("network: 未知策略 %q", s.cfg.EffectiveNetwork())
	}
	return nil
}

// buildExecArgv 把单工请求解析成最终命令行。
//
// 两条路径，互斥：
//   - 语言路径（lang 非空）：argv = [解释器, 脚本, 参数...]，解释器由 resolve 按当前
//     mode 解析 —— 「镜像里叫什么」由此只保留 sandboxd 一处认知；
//   - 直传路径（lang 为空）：argv 由调用方完全决定（shell 命令、自带解释器），一字不改。
//
// resolve 以回调注入，便于单测（不必构造 Server / 依赖配置）。
func buildExecArgv(lang string, argv []string, script string, args []string,
	resolve func(string) (string, error)) ([]string, error) {

	if lang == "" {
		if len(argv) == 0 {
			return nil, errors.New("empty argv")
		}
		return argv, nil
	}
	// 同时给 lang 与 argv 会拼出不可预期的命令（如 `lua lua /x.lua`），直接拒绝。
	if len(argv) > 0 {
		return nil, fmt.Errorf("lang %q and argv are mutually exclusive", lang)
	}
	if script == "" {
		return nil, fmt.Errorf("lang %q requires a script path", lang)
	}
	cmd, err := resolve(lang)
	if err != nil {
		return nil, err
	}
	return append([]string{cmd, script}, args...), nil
}
