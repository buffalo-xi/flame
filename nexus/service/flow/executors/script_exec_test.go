package executors

import (
	"errors"
	"fmt"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/capability"
	"flame/tools/code"
)

// 「沙箱繁忙」必须独立成码：它是一次可重试的瞬时拒绝（脚本压根没执行），
// 一旦被折回 script，on_error.retry.codes 就永远配不上（见 scriptErrCode 的说明）。
func TestScriptErrCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"繁忙", code.NewError(code.ErrCodeBusy, "sandbox busy: lua 并发已达上限 32", nil), flow.ErrCodeBusy},
		{"繁忙（经 fmt.Errorf 包裹）", fmt.Errorf("handler: %w",
			code.NewError(code.ErrCodeBusy, "sandbox busy", nil)), flow.ErrCodeBusy},
		{"脚本自身报错", code.NewError(code.ErrCodeScriptError, "boom", nil), flow.ErrCodeScript},
		{"连接失败", code.NewError(code.ErrCodeConnectFail, "dial: refused", nil), flow.ErrCodeScript},
		{"非脚本错误", errors.New("code client not initialized"), flow.ErrCodeScript},
		// 平台未开通的代码片段能力：必须单独成码，不能被 on_error 当成脚本问题重试
		{"能力未开通", &capabilityError{capability: capability.Code}, flow.ErrCodeCapability},
		{"能力未开通（经包裹）", fmt.Errorf("handler: %w", &capabilityError{capability: capability.Code}), flow.ErrCodeCapability},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scriptErrCode(tc.err); got != tc.want {
				t.Errorf("scriptErrCode() = %q, want %q", got, tc.want)
			}
		})
	}
}
