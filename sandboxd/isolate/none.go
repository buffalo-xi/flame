package isolate

import (
	"context"
	"os/exec"

	"flame/tools/sandbox"
)

// noneSandbox 直通实现：不做任何隔离，等价于直接 exec.Command。
//
// 由于没有隔离，argv/dir 中的「沙箱内路径」会按 Mounts 翻译回宿主路径后再执行，
// 这样调用方只需写一套路径。
//
// 资源上限：Limits 里只有 Timeout 是真的（ctx 兜得住），MemoryMB / CPUCount / MaxPIDs /
// MaxFileMB 在这一档全部无从施加 —— 没有 cgroup / namespace / Job Object 可用。
// 这是「宿主直通」的定义，不是漏实现；启动时会在 sandboxd 侧打一条日志说明。
type noneSandbox struct{}

// NewNone 创建直通隔离实现（仅用于开发/调试）。
func NewNone() sandbox.Sandbox { return &noneSandbox{} }

func (s *noneSandbox) Name() string { return NoneName }

func (s *noneSandbox) Start(ctx context.Context, spec *sandbox.Spec) (sandbox.Process, error) {
	if len(spec.Argv) == 0 {
		return nil, errEmptyArgv
	}

	// 超时：仅当指定时包一层；cancel 由 startWithPipes 挂到进程上，等结束再释放
	var cancel context.CancelFunc
	if spec.Limits.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, spec.Limits.Timeout)
	}

	// 把「沙箱内路径」翻译回宿主路径
	argv := TranslateArgv(spec.Mounts, spec.Argv)
	dir := TranslateToHost(spec.Mounts, spec.Dir)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}

	// kill 传 nil：用默认收口（进程组）。none 没有 cgroup / namespace，
	// 这是它唯一的手段 —— 因此它的保证只是「组内干净」，不是「杀掉全部后代」。
	return startWithPipes(cmd, spec, cancel, nil)
}
