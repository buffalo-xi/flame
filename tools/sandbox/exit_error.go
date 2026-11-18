package sandbox

import "fmt"

// ExitError 沙箱内进程以非零码退出。
//
// 存在的理由：*exec.ExitError 内部的 ProcessState 不可导出，
// 远端实现（remote）无法构造它。因此统一用本类型表达「非零退出码」，
// 本地实现（process.go 的 execProcess.Wait）也归一化成它，
// 调用方只需处理一种错误类型。
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// ExitCode 返回退出码（与 *exec.ExitError 同形，便于调用方统一处理）。
func (e *ExitError) ExitCode() int { return e.Code }
