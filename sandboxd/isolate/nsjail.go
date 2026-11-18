package isolate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"flame/tools/sandbox"
)

// nsjailPath jail 内的 PATH：nsjail 用 execve 直接执行命令、不解析 PATH，
// 所以既要把这份 PATH 通过 --env 传给 jail 内进程（codehost fork 解释器要依赖它），
// 也要用它把纯命令名解析成 rootfs 内的绝对路径（见 resolveRootfsCmd）。
const nsjailPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// nsjailSandbox 通过 nsjail 在 Linux namespace/cgroup 内执行命令（仅 Linux）。
//
// 优点：启动开销极低（~10ms），与池化（长驻 worker）天然契合。
// 缺点：需要自行准备 rootfs（含解释器与依赖），且不能在 Windows 上直接运行。
type nsjailSandbox struct {
	opts Options
}

// NewNsjail 创建 nsjail 隔离实现。opts.Rootfs 必填。
func NewNsjail(opts Options) sandbox.Sandbox {
	if opts.Binary == "" {
		opts.Binary = NsjailName
	}
	return &nsjailSandbox{opts: opts}
}

func (s *nsjailSandbox) Name() string { return NsjailName }

func (s *nsjailSandbox) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}
	if s.opts.Rootfs == "" {
		return nil, fmt.Errorf("isolate/nsjail: rootfs is required")
	}

	lim := sandbox.MergeLimits(s.opts.Limits, spec.Limits)

	var cancel context.CancelFunc
	if lim.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout)
		// cancel 交给 startWithPipes 在进程结束后释放，避免提前终止 jail
	}

	args := []string{"-Mo", "--chroot", s.opts.Rootfs}

	// 挂载：Host → Guest
	for _, m := range spec.Mounts {
		if m.Host == "" || m.Guest == "" {
			continue
		}
		flag := "--bindmount"
		if m.ReadOnly {
			flag = "--bindmount_ro"
		}
		args = append(args, flag, filepath.ToSlash(m.Host)+":"+m.Guest)
	}

	if spec.Dir != "" {
		args = append(args, "--cwd", spec.Dir)
	}
	if s.opts.UID > 0 {
		args = append(args, "--user", strconv.Itoa(s.opts.UID))
	}
	if s.opts.GID > 0 {
		args = append(args, "--group", strconv.Itoa(s.opts.GID))
	}
	// jail 内 PATH：nsjail 默认不继承父进程环境变量（不显式 --env 时 jail 里的
	// PATH 是空的），而 codehost fork 解释器用的是命令名（"lua"/"python3"/"node"，
	// 见 langs.*.command 与 ResolveCommand），必须靠 PATH 在 rootfs 里解析。
	// 给一个覆盖 rootfs 标准位置（/usr/local/bin 软链、/usr/bin 二进制）的最小 PATH。
	args = append(args, "--env", "PATH="+nsjailPath)
	// rootfs 只读：nsjail 的 chroot 默认就是 R/O（--rw 才可写），
	// 所以 ReadOnlyRootfs=true 不需要追加任何参数，只有显式要求可写时才加 --rw。
	//
	// 这里此前误映射成 `--disable_proc`，而它的语义是 "Disable mounting procfs
	// in the jail"，与 rootfs 只读无关（且 config.proto 里 mount_proc 默认即为
	// false，该参数基本是空操作）。后果是 read_only_rootfs=false 静默失效 ——
	// 谁想关掉只读都会踩到，故改为直译。
	if !s.opts.ReadOnlyRootfs {
		args = append(args, "--rw")
	}
	// 断网：spec.Egress 是**裁决后**的意图（策略在 handleExec 里判），nil = 跟配置。
	//
	// ⚠️ egress=true 在这里只是「不主动断绝」：nsjail 默认 clone_newnet，jail 里本来
	// 就没有对外接口，真出网要靠 extra_args 里的 --disable_clone_newnet（配置负责，
	// 见 validateNetworkOnRequest 的告警）。isolate 不判断这件事，也不该判断。
	disableNet := s.opts.DisableNetwork
	if spec.Egress != nil {
		disableNet = !*spec.Egress
	}
	if disableNet {
		args = append(args, "--iface_no_lo")
	}

	// 资源上限
	// 内存：用 cgroup 的物理内存上限（--cgroup_mem_max，字节）而不是 --rlimit_as ——
	// 后者限的是虚拟地址空间 VSZ，Go/V8 等运行时会预留几十 GB 虚拟内存，会被误杀。
	// --cgroup_mem_max 在 v1/v2 下都有效，版本由 cgroup_version 配置决定。
	if lim.MemoryMB > 0 {
		switch s.opts.CgroupVersion {
		case CgroupV2:
			args = append(args, "--use_cgroupv2")
		case CgroupAuto:
			args = append(args, "--detect_cgroupv2")
		// CgroupV1 或空：不加版本参数，走 nsjail 默认（v1）
		}
		args = append(args, "--cgroup_mem_max", strconv.Itoa(lim.MemoryMB*1024*1024))
	}
	if lim.CPUCount > 0 {
		args = append(args, "--max_cpus", strconv.Itoa(lim.CPUCount))
	}
	if lim.MaxPIDs > 0 {
		args = append(args, "--rlimit_nproc", strconv.Itoa(lim.MaxPIDs))
	}
	if lim.Timeout > 0 {
		args = append(args, "--time_limit", strconv.Itoa(int(lim.Timeout.Seconds())))
	}

	args = append(args, s.opts.ExtraArgs...)
	args = append(args, "--")
	// nsjail 的 execve 不解析 PATH，纯命令名（如 "cat"、"python3"）会报
	// No such file or directory。这里把 argv[0] 解析成 rootfs 内的绝对路径；
	// 含斜杠的（已是路径）原样保留。
	argv := append([]string(nil), spec.Argv...)
	argv[0] = resolveRootfsCmd(s.opts.Rootfs, argv[0])
	args = append(args, argv...)

	// kill 传 nil：用默认收口（进程组）。nsjail 进程本身及其派生都在宿主
	// 进程树里，进程组能覆盖；若实测发现 nsjail 自己 setsid 脱离，这里要换成
	// 它的 cgroup 参数（见 sysproc_unix.go 的 killTree 注释）。
	cmd := exec.CommandContext(ctx, s.opts.Binary, args...)
	return startWithPipes(cmd, spec, cancel, nil)
}

// resolveRootfsCmd 把纯命令名解析成 rootfs 内的绝对路径。
//
// nsjail 用 execve 直接执行 argv[0]、不解析 PATH，所以纯命令名会 ENOENT。
// 这里在 rootfs 里按 nsjailPath 逐目录查找，返回 jail 内的绝对路径
// （如 /usr/bin/cat）；找不到或本就含斜杠时原样返回，让 nsjail 报它自己的错误。
func resolveRootfsCmd(rootfs, cmd string) string {
	if cmd == "" || rootfs == "" || strings.ContainsRune(cmd, '/') {
		return cmd
	}
	for _, dir := range strings.Split(nsjailPath, ":") {
		guest := filepath.Join(dir, cmd)     // jail 内路径
		host := filepath.Join(rootfs, guest) // 宿主侧路径（rootfs 前缀）
		if fi, err := os.Stat(host); err == nil && !fi.IsDir() {
			return guest
		}
	}
	return cmd
}
