// Package isolate 提供具体的进程隔离实现（none / nsjail / docker）。
//
// 这些实现只由 sandboxd 使用：nexus 侧不做隔离，只通过 sandbox.NewRemote
// 把执行委托给 sandboxd。所有实现均满足 sandbox.Sandbox 接口。
package isolate

import (
	"errors"
	"fmt"
	"os/exec"

	"flame/tools/sandbox"
)

// 隔离实现名（与配置中的 mode 对应）。
const (
	NoneName   = "none"
	NsjailName = "nsjail"
	DockerName = "docker"
)

// cgroup 版本选择（nsjail 内存限制用）。--cgroup_mem_max 在 v1/v2 下都有效
// （分别写 memory.limit_in_bytes / memory.max，都限物理内存而非 VSZ），
// 这里只决定「告诉 nsjail 用哪个版本」的参数。
const (
	CgroupV1   = "v1"   // 不加版本参数，走 nsjail 默认（cgroup v1）
	CgroupV2   = "v2"   // --use_cgroupv2（宿主必须是 v2）
	CgroupAuto = "auto" // --detect_cgroupv2：有 v2 用 v2，否则回退 v1
)

// errEmptyArgv 未提供命令行。
var errEmptyArgv = errors.New("isolate: empty argv")

// Options 隔离实现的构造参数。
type Options struct {
	// Limits 默认资源上限；Spec.Limits 中非零字段会覆盖它。
	Limits sandbox.Limits

	// ---- nsjail ----
	Binary         string // nsjail 可执行文件，默认 "nsjail"
	Rootfs         string // 沙箱根文件系统（必填）
	UID            int    // 沙箱内用户 id
	GID            int    // 沙箱内组 id
	DisableNetwork bool   // 关闭网络
	ReadOnlyRootfs bool   // 根文件系统只读；nsjail 的 chroot 默认即 R/O，置 false 才加 --rw
	CgroupVersion  string // cgroup 版本选择：v1 / v2 / auto

	// ---- docker ----
	Image    string // 容器镜像（必填）
	Network  string // none / bridge / host
	ReadOnly bool   // 容器 rootfs 只读
	User     string // 容器内用户，如 "1000:1000"

	// ExtraArgs 追加到各实现原生参数末尾（逃生口）。
	ExtraArgs []string
}

// New 按模式名创建隔离实现。
//
// 创建时会做依赖自检（LookPath），让环境问题在启动阶段就暴露，
// 而不是等第一次执行脚本/命令时才失败。
func New(mode string, opts Options) (sandbox.Sandbox, error) {
	switch mode {
	case "", NoneName:
		return NewNone(), nil

	case NsjailName:
		if opts.Rootfs == "" {
			return nil, fmt.Errorf("isolate: nsjail requires rootfs")
		}
		if err := lookPath(opts.Binary, NsjailName); err != nil {
			return nil, err
		}
		return NewNsjail(opts), nil

	case DockerName:
		if opts.Image == "" {
			return nil, fmt.Errorf("isolate: docker requires image")
		}
		if err := lookPath(opts.Binary, DockerName); err != nil {
			return nil, err
		}
		return NewDocker(opts), nil

	default:
		return nil, fmt.Errorf("isolate: unknown mode %q", mode)
	}
}

// lookPath 校验可执行文件可用（bin 为空时用 fallback）。
func lookPath(bin, fallback string) error {
	if bin == "" {
		bin = fallback
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("isolate: %s binary not found (%q): %w", fallback, bin, err)
	}
	return nil
}
