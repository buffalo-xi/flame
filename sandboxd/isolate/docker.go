package isolate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"flame/tools/sandbox"
)

// dockerSandbox 通过 docker run 在容器内执行命令（跨平台）。
//
// 说明：容器与宿主共享内核，隔离强度与 nsjail 同级；优势是镜像生态与跨平台，
// 代价是每次启动约 300ms–1s。对启动开销敏感的场景优先用 nsjail。
type dockerSandbox struct {
	opts Options
}

// NewDocker 创建 docker 隔离实现。opts.Image 必填。
func NewDocker(opts Options) sandbox.Sandbox {
	if opts.Binary == "" {
		opts.Binary = DockerName
	}
	if opts.Network == "" {
		opts.Network = "none"
	}
	return &dockerSandbox{opts: opts}
}

func (s *dockerSandbox) Name() string { return DockerName }

func (s *dockerSandbox) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}
	if s.opts.Image == "" {
		return nil, fmt.Errorf("isolate/docker: image is required")
	}

	lim := sandbox.MergeLimits(s.opts.Limits, spec.Limits)

	var cancel context.CancelFunc
	if lim.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout)
		// cancel 交给 startWithPipes 在进程结束后释放，避免提前杀掉容器
	}

	// 容器身份由我们命名：这是这次执行唯一可寻址的入口，
	// 「杀干净」全看它 —— 见下面 kill 的注释。
	name, err := newExecContainerName()
	if err != nil {
		releaseCancel(cancel)
		return nil, err
	}

	args := []string{"run", "--rm", "-i", "--name", name, "--label", execLabel}
	// 容器级参数（网络 / 只读 / 用户 / 资源上限 / 挂载 / cwd / env）与长驻单元
	// 共用同一份构造 —— 单次 run 与复用单元必须在隔离强度上完全一致。
	args = append(args, s.containerArgs(spec, lim)...)
	args = append(args, s.opts.ExtraArgs...)
	args = append(args, s.opts.Image)
	args = append(args, spec.Argv...)

	cmd := exec.CommandContext(ctx, s.opts.Binary, args...)

	// 这次执行的收口：先让容器消失，再收掉 CLI 进程。
	//
	// 为什么不能只杀 CLI（cmd.Process.Kill / 进程组都不够）：容器由
	// dockerd / containerd-shim 托管，CLI 只是一根通往容器的管子 —— 杀它
	// 等于断管，容器照跑（`docker run -d` 就是「CLI 退出、容器照跑」的活证据；
	// --sig-proxy 也救不了，SIGKILL 不可捕获、无从转发）。
	// 容器即 cgroup，成员由内核记账：rm -f 连 setsid 跑掉的进程一起收，
	// 这是进程组做不到的。
	kill := func() error {
		rmErr := s.removeContainer(name)
		cliErr := killTree(cmd)
		if rmErr != nil {
			return rmErr
		}
		if cliErr != nil && !errors.Is(cliErr, os.ErrProcessDone) {
			return cliErr
		}
		return nil
	}
	return startWithPipes(cmd, spec, cancel, kill)
}

// isEnvKey 判断环境变量名是否合法（字母/下划线开头，仅含字母数字下划线）。
func isEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// execContainerNamePrefix 一次性（exec 路径）容器的名字前缀。
const execContainerNamePrefix = "sandboxd-exec-"

// newExecContainerName 生成一个唯一的容器名。
//
// 为什么用名字而不是 --cidfile：名字在 docker run 的命令行里就生效，容器一起来
// 就能按名字 rm -f，不需要任何本地文件、也就没有文件的生命周期要管。
// 名字撞车（理论上只可能来自上次崩溃残留）时 docker 会明确报错，不会静默错杀。
func newExecContainerName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("isolate/docker: generate container name: %w", err)
	}
	return execContainerNamePrefix + hex.EncodeToString(b[:]), nil
}

// removeContainer 让容器消失 —— docker 模式下「杀干净」的收口。
//
// 先按名字查再删：容器可能已经自己退出（--rm 已删），那不是错误，
// 也不该让调用方看到一条「No such container」。
func (s *dockerSandbox) removeContainer(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), containerKillTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, s.opts.Binary, "ps", "-aq", "--filter", "name=^"+name+"$").Output()
	if err != nil {
		return fmt.Errorf("isolate/docker: locate container %s: %w", name, err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}

	args := append([]string{"rm", "-f"}, ids...)
	if rmOut, err := exec.CommandContext(ctx, s.opts.Binary, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("isolate/docker: remove container %s: %w (%s)", name, err, strings.TrimSpace(string(rmOut)))
	}
	return nil
}
