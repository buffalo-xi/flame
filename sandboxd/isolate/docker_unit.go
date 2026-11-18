package isolate

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flame/tools/sandbox"
)

// unitLabel 打在每个复用单元（容器）上的标记。
//
// 两处用到：
//   - CleanOrphans 按它找出上次异常退出遗留的容器；
//   - 排障时 `docker ps --filter label=sandboxd=1` 一眼看出哪些是本服务的。
const unitLabel = "sandboxd=1"

// execLabel 打在每个一次性（exec 路径）容器上的标记。
//
// 为什么需要：没有标记的容器对 sandboxd 是隐形的 —— 泄漏之后连 CleanOrphans
// 都扫不到，只能等它自己退出（而 `--rm` 只管容器退出时删除，不管服务死活）。
// 有了它，强杀 sandboxd 留下的 exec 容器在下次启动时能被一起回收。
//
// ⚠️ 与 unitLabel 同一个前提：本服务假设独占这个 docker 守护进程。
// 多实例共用一个 daemon 时，启动清扫会把兄弟实例的在途容器一起删掉。
const execLabel = "sandboxd-exec=1"

// orphanLabels 启动清扫要回收的全部容器标记。
var orphanLabels = []string{unitLabel, execLabel}

// containerKillTimeout 「让容器消失」这条路径自身的上限：
// 杀进程不能变成新的阻塞点（docker 守护进程卡住时也要能返回）。
const containerKillTimeout = 10 * time.Second

// Acquire 实现 sandbox.Reusable：起一个长驻容器作为可复用单元。
//
// 与 Start 的区别（见 nexus/docs/sandbox-design.md §9.3）：
//   - 后台常驻（-d），容器命令固定 sleep infinity —— 它只是隔离边界，
//     解释器由后续每次 Exec 现起；
//   - --init：容器 PID 1 换成 tini。否则脚本 fork 出的孤儿/僵尸会堆在
//     sleep infinity 底下没人回收；
//   - --label：留下可识别标记，供启动时的孤儿回收（CleanOrphans）；
//   - **不加 -t**：TTY 的行处理会破坏 4 字节长度前缀的二进制帧协议。
//
// 注意：docker exec 不支持 -v，可写目录只能在建容器时挂好，
// 每请求的子目录要建在已挂载的挂载点之下（见 Exec 的 spec.Dir）。
func (s *dockerSandbox) Acquire(ctx context.Context, spec *sandbox.Spec) (sandbox.Unit, error) {
	if s.opts.Image == "" {
		return nil, fmt.Errorf("isolate/docker: image is required")
	}
	if spec == nil {
		spec = &sandbox.Spec{}
	}

	lim := sandbox.MergeLimits(s.opts.Limits, spec.Limits)

	args := []string{"run", "-d", "--rm", "--init", "--label", unitLabel}
	args = append(args, s.containerArgs(spec, lim)...)
	args = append(args, s.opts.ExtraArgs...)
	args = append(args, s.opts.Image, "sleep", "infinity")

	out, err := exec.CommandContext(ctx, s.opts.Binary, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("isolate/docker: acquire unit: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return nil, fmt.Errorf("isolate/docker: acquire unit: empty container id")
	}
	return &dockerUnit{opts: s.opts, id: id}, nil
}

// containerArgs 组装「容器级」参数：网络 / 只读 / 用户 / 资源上限 / 挂载 / cwd / env。
//
// Start 与 Acquire 共用这一份，避免两处参数漂移 —— 单次 run 与长驻单元
// 在隔离参数上必须完全一致，否则「同一个镜像、两种强度」这种坑很难查。
func (s *dockerSandbox) containerArgs(spec *sandbox.Spec, lim sandbox.Limits) []string {
	var args []string

	// 网络：spec.Egress 是**裁决后**的意图（策略在 handleExec 里判），nil = 跟配置。
	// isolate 不做策略判断，只负责把「这次要不要断网」翻译成参数。
	//
	// 注意 egress=true 时用的是配置里的网络（NewDocker 已把空值补成 none）：
	// 若配置真是 none，说明裁决层漏了，这里保持 none —— 宁可断网，不做意外出网。
	network := s.opts.Network
	if spec.Egress != nil && !*spec.Egress {
		network = "none"
	}
	args = append(args, "--network", network)
	if s.opts.ReadOnly {
		args = append(args, "--read-only")
	}
	if s.opts.User != "" {
		args = append(args, "--user", s.opts.User)
	}

	if lim.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(lim.MemoryMB)+"m")
	}
	if lim.CPUCount > 0 {
		args = append(args, "--cpus", strconv.Itoa(lim.CPUCount))
	}
	if lim.MaxPIDs > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(lim.MaxPIDs))
	}

	// 挂载（宿主路径统一转斜杠，兼容 Windows）
	hasTmpMount := false
	for _, m := range spec.Mounts {
		if m.Host == "" || m.Guest == "" {
			continue
		}
		if m.Guest == "/tmp" {
			hasTmpMount = true
		}
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		args = append(args, "-v", filepath.ToSlash(m.Host)+":"+m.Guest+":"+mode)
	}

	// 只读 rootfs 下 /tmp 也是只读的 —— 而它是唯一一个「所有镜像都有、
	// 又天然该可写」的通用位置：浏览器要把 Chrome profile 放进去（--tmpfs 随容器
	// 销毁，不会漏进工作区），shell 会话里临时写文件也靠它。
	// 已经显式挂了 /tmp 的调用方（脚本 worker 把独占目录挂到 /tmp）不再覆盖。
	// size 是硬上限：tmpfs 计进容器内存，不设限会被一个下载/缓存撑爆 cgroup。
	if s.opts.ReadOnly && !hasTmpMount {
		args = append(args, "--tmpfs", "/tmp:rw,size=256m,mode=1777")
	}

	if spec.Dir != "" {
		args = append(args, "-w", spec.Dir)
	}
	args = append(args, envArgs(spec.Env)...)
	return args
}

// envArgs 过滤并展开环境变量。
// 跳过非标识符开头的键（Windows 上存在 =C: 之类，传给 docker 会报错）。
func envArgs(env []string) []string {
	var args []string
	for _, e := range env {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		if !isEnvKey(e[:eq]) {
			continue
		}
		args = append(args, "-e", e)
	}
	return args
}

// dockerUnit 一个常驻容器构成的沙箱单元。
type dockerUnit struct {
	opts Options
	id   string
}

func (u *dockerUnit) Name() string { return u.id }

// Exec 在已有容器内启动一个进程（docker exec）。
//
// 与 run 的差别：exec 只接受 -i / -w / -e / -u 这类「进程级」参数；
// 网络、只读、资源上限都在建容器时定死（容器级），exec 出来的进程继承它们。
//
// 返回的 Process 与 Start 的语义一致，所以帧协议那一层完全不需要区分 L2/L3。
func (u *dockerUnit) Exec(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if spec == nil || len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}

	var cancel context.CancelFunc
	if spec.Limits.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spec.Limits.Timeout)
	}

	args := []string{"exec", "-i"}
	if spec.Dir != "" {
		args = append(args, "-w", spec.Dir)
	}
	args = append(args, envArgs(spec.Env)...)
	args = append(args, u.id)
	args = append(args, spec.Argv...)

	// kill 传 nil：杀的是 docker exec 的 CLI 进程（进程组），而它只是
	// 「从外部进入单元」的通道 —— 单元内的解释器不一定会跟着死。
	// 单元内的补刀由上层负责（见 daemon 的 killResidual），这里不做。
	cmd := exec.CommandContext(ctx, u.opts.Binary, args...)
	return startWithPipes(cmd, spec, cancel, nil)
}

// Close 销毁容器。用 -f：单元内可能仍有没退干净的进程，先杀再删。
//
// ⚠️ 只在「销毁单元」时调用。杀「单元内的解释器」不能用它 ——
// 那会把好不容易建起来的隔离边界一起扔掉，等于退回 L3。
func (u *dockerUnit) Close() error {
	if u.id == "" {
		return nil
	}
	out, err := exec.Command(u.opts.Binary, "rm", "-f", u.id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("isolate/docker: close unit %s: %w (%s)", u.id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CleanOrphans 实现 sandbox.OrphanCleaner：回收上次异常退出遗留的容器。
//
// 必要性：--rm 只保证「容器退出时自动删」，不保证「服务退出时容器退出」。
// sandboxd 被强杀（或宿主断电）时容器来不及 Close / removeContainer，
// 会一直占着内存。启动时按 label 各扫一遍，代价是两次 docker ps。
//
// 覆盖两类：复用单元（unitLabel）与一次性 exec 容器（execLabel）——
// 后者是「杀干净」改造之后才可寻址的（此前它连标签都没有，泄漏了没人看得见）。
func (s *dockerSandbox) CleanOrphans(ctx context.Context) (int, error) {
	total := 0
	for _, label := range orphanLabels {
		n, err := s.removeByLabel(ctx, label)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// removeByLabel 按 label 回收容器，返回清理数量。
func (s *dockerSandbox) removeByLabel(ctx context.Context, label string) (int, error) {
	out, err := exec.CommandContext(ctx, s.opts.Binary, "ps", "-aq", "--filter", "label="+label).Output()
	if err != nil {
		return 0, fmt.Errorf("isolate/docker: list orphans (%s): %w", label, err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return 0, nil
	}

	args := append([]string{"rm", "-f"}, ids...)
	if rmOut, err := exec.CommandContext(ctx, s.opts.Binary, args...).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("isolate/docker: remove orphans (%s): %w (%s)", label, err, strings.TrimSpace(string(rmOut)))
	}
	return len(ids), nil
}
