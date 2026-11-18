// Command sandboxd 是独立的沙箱服务：接收执行请求，在配置的隔离方式
// （none / nsjail / docker）内运行命令与脚本。
//
// 职责边界：
//   - nexus 侧不做隔离、不持有池，只提交单工/双工请求；
//   - 隔离方式、资源上限、进程池与并发额度，全部由本服务配置决定。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"flame/sandboxd/internal/daemon"
	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
	"flame/tools/xlog"
	"flame/tools/xregistry"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := daemon.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// 初始化日志（xlog 的 Logger 需先 Init，否则 Info/Error 会 panic）
	if err := xlog.Init(cfg.LogDir, "sandboxd"); err != nil {
		fmt.Fprintln(os.Stderr, "sandboxd: init log:", err)
		os.Exit(1)
	}

	// 配置期的提醒（目前只有旧键回填）：必须等 xlog 就绪才打得出来。
	// 这类提醒唯一的用处就是被看见 —— 旧键会一直生效，但下次改配置时应当换成新键。
	for _, w := range cfg.Warnings {
		xlog.Error("sandboxd: %s", w)
	}

	// 创建隔离实现（会做 nsjail/docker 可执行文件自检）
	iso, err := isolate.New(cfg.Mode, isolate.Options{
		Binary:         binaryFor(cfg),
		Rootfs:         cfg.Nsjail.Rootfs,
		UID:            cfg.Nsjail.UID,
		GID:            cfg.Nsjail.GID,
		DisableNetwork: cfg.Nsjail.DisableNetwork,
		ReadOnlyRootfs: cfg.Nsjail.ReadOnlyRootfs,
		CgroupVersion:  cfg.Nsjail.CgroupVersion,
		Image:          cfg.Docker.Image,
		Network:        cfg.Docker.Network,
		ReadOnly:       cfg.Docker.ReadOnly,
		User:           cfg.Docker.User,
		ExtraArgs:      extraArgsFor(cfg),
	})
	if err != nil {
		xlog.Error("sandboxd: init isolate: %v", err)
		os.Exit(1)
	}

	if len(cfg.AllowedMountRoots) == 0 {
		xlog.Error("sandboxd: allowed_mount_roots is empty — mount paths will NOT be validated")
	}
	if cfg.Token == "" {
		xlog.Error("sandboxd: token is empty — no authentication on /v1 endpoints")
	}
	if cfg.Mode == isolate.NoneName {
		xlog.Error("sandboxd: mode=none — commands run WITHOUT isolation")
	}
	// 网络声明：把生效档位与 mode 一起打进日志。「配置里写的」与「实际生效的」
	// 必须一眼可见（与 langs 按 mode 解析入口名同一做法）。
	xlog.Info("sandboxd: network policy mode=%s policy=%s", cfg.Mode, cfg.EffectiveNetwork())

	// 交互硬闸：脚本执行的实际上限就是它（请求下发的超时只能收紧、不能放宽）。
	// 不打印的话，nexus 侧把 script.<lang>.timeout 配成 600 这种「配了等于没配」
	// 的情况只能靠读代码发现。
	xlog.Info("sandboxd: script timeout hard cap = %ds (code_max_sec); a request timeout can only lower it",
		cfg.CodeMaxSec)

	// mode=none 时命令由宿主直接执行，必须能在宿主解析 —— 启动即校验，
	// 避免等到第一次调用脚本才失败（其他模式下命令在沙箱内部解析，此处跳过）
	if err := checkCommands(cfg); err != nil {
		xlog.Error("sandboxd: %v", err)
		fmt.Fprintln(os.Stderr, "sandboxd:", err)
		os.Exit(1)
	}

	// 回收上次异常退出遗留的单元：--rm 只保证「容器退出时自动删」，不保证
	// 「服务退出时容器退出」—— 被强杀时残留的单元会一直占着内存。
	if err := cleanOrphans(iso); err != nil {
		xlog.Error("sandboxd: clean orphan units: %v", err)
	}

	srv, err := daemon.NewServer(cfg, iso)
	if err != nil {
		xlog.Error("sandboxd: %v", err)
		fmt.Fprintln(os.Stderr, "sandboxd:", err)
		os.Exit(1)
	}
	defer srv.Close()

	xlog.Info("sandboxd: listening on %s (mode=%s)", cfg.Listen, cfg.Mode)

	// 这里用 http.Server 而不是 gin 的 Run：Run 在内部自建 server 且阻塞返回，
	// 外面拿不到句柄，收到信号时没法优雅关闭。
	httpSrv := &http.Server{Addr: cfg.Listen, Handler: srv.Router()}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			xlog.Error("sandboxd: server exited: %v", err)
			os.Exit(1)
		}
	}()

	// 服务注册（可选）：注册到 consul/nacos，供 sandboxg 发现。放在 HTTP 起来之后，
	// 让 consul 的 /healthz 探活第一次就成功。
	var registrar xregistry.Registrar
	if cfg.Registry.Enabled {
		rc := registryConfig(cfg)
		reg, err := xregistry.NewRegistrar(rc)
		if err != nil {
			xlog.Error("sandboxd: create registrar: %v", err)
		} else {
			registrar = reg
			if err := reg.Register(); err != nil {
				xlog.Error("sandboxd: register: %v (will retry via KeepRegistered)", err)
			} else {
				xlog.Info("sandboxd: registered to %s as service %s", rc.Type, rc.ServiceName)
			}
			// 保活自愈：定期确认注册状态，被摘除（注册中心断连）或初始失败都自愈。
			reg.KeepRegistered(ctx, 30*time.Second)
		}
	}

	// 优雅关闭 —— L2 的必要配套。
	//
	// L3 的前台进程（docker run --rm -i）会随 stdin 关闭自退，容器跟着消失；
	// 而 L2 的单元是 docker run -d 的长驻容器，生命周期独立于客户端，
	// 不显式 Close 就会留下 N 个 sleep infinity，只能等下次启动的
	// CleanOrphans 兜底 —— 那是崩溃兜底，不该成为常规停机路径。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	cancel() // 让 KeepRegistered 等后台循环退出
	xlog.Info("sandboxd: signal received, closing units ...")
	if registrar != nil {
		_ = registrar.Deregister()
	}
	srv.Close()
	_ = httpSrv.Close()
	xlog.Info("sandboxd: bye")
}

// binaryFor 按模式取可执行文件路径。
func binaryFor(cfg *daemon.Config) string {
	switch cfg.Mode {
	case isolate.NsjailName:
		return cfg.Nsjail.Binary
	case isolate.DockerName:
		return cfg.Docker.Binary
	default:
		return ""
	}
}

// extraArgsFor 按模式取逃生参数。
func extraArgsFor(cfg *daemon.Config) []string {
	switch cfg.Mode {
	case isolate.NsjailName:
		return cfg.Nsjail.ExtraArgs
	case isolate.DockerName:
		return cfg.Docker.ExtraArgs
	default:
		return nil
	}
}

// checkCommands 在 mode=none 下校验各语言的命令可在宿主解析。
//
// 原因：mode=none 时命令由宿主直接 exec，PATH 里没有就会在第一次调用时
// 才失败（错误很难定位）。nsjail/docker 模式下命令在 rootfs/镜像内部解析，
// 宿主 LookPath 会误报，因此只在 none 下校验。
//
// ⚠️ 校验的必须是 ResolveCommand 的结果（当前 mode 实际要跑的那个命令），
// 不是通用的 lc.Command —— 否则配了 commands.none 的部署会被误报，
// 或者压根校验了一个不会执行的命令。
func checkCommands(cfg *daemon.Config) error {
	if cfg.Mode != isolate.NoneName {
		return nil
	}
	// 遍历顶层语言定义而不是 code.pools：语言定义现在同时服务单工与双工，
	// 而 mode=none 下两类用途都在宿主 exec，所以每个定义的入口名都得能解析。
	for lang, def := range cfg.Langs {
		cmd, err := def.ResolveCommand(cfg.Mode)
		if err != nil {
			return fmt.Errorf("langs.%s: %w", lang, err)
		}
		if _, err := exec.LookPath(cmd); err != nil {
			return fmt.Errorf(
				"langs.%s: command %q not found on host — mode=none needs a host-resolvable binary "+
					"(use a PATH name or an absolute path, e.g. \"d:/tools/lua5.4/lua54.exe\")",
				lang, cmd)
		}
	}
	return nil
}

// cleanOrphans 回收隔离实现留下的孤立容器。
//
// 只有实现了 sandbox.OrphanCleaner 的实现才有这个概念（目前是 docker）：
// 服务被强杀时复用单元来不及 Close、一次性容器来不及 removeContainer，
// 而 --rm 不会替我们收拾（它只管容器自己退出）。
//
// 覆盖两类：复用单元 + 一次性 exec 容器（后者带 sandboxd-exec 标签，
// 是「杀干净」改造之后才可寻址的 —— 此前它连标签都没有，泄漏了没人看得见）。
func cleanOrphans(iso sandbox.Sandbox) error {
	oc, ok := iso.(sandbox.OrphanCleaner)
	if !ok {
		return nil
	}
	n, err := oc.CleanOrphans(context.Background())
	if err != nil {
		return err
	}
	if n > 0 {
		xlog.Info("sandboxd: cleaned %d orphan container(s) left by a previous run", n)
	}
	return nil
}

// registryConfig 把 sandboxd 的 registry 配置转成 xregistry 的注册配置。
// ServiceAddr 必须显式给出 gateway 能连到的地址（listen 可能是 127.0.0.1，跨机
// 不可达）；ServicePort 缺省从 listen 解析。
func registryConfig(cfg *daemon.Config) *xregistry.RegistrarConfig {
	name := cfg.Registry.ServiceName
	if name == "" {
		name = "sandboxd"
	}
	path := cfg.Registry.HealthCheckPath
	if path == "" {
		path = "/healthz"
	}
	port := cfg.Registry.ServicePort
	if port == 0 {
		if _, p, err := net.SplitHostPort(cfg.Listen); err == nil {
			port, _ = strconv.Atoi(p)
		}
	}
	return &xregistry.RegistrarConfig{
		Type:            xregistry.Type(cfg.Registry.Type),
		Address:         cfg.Registry.Address,
		ServiceName:     name,
		ServiceAddr:     cfg.Registry.ServiceAddr,
		ServicePort:     port,
		HealthCheckPath: path,
	}
}
