package executors

import (
	"errors"
	"fmt"
	"testing"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/capability"
	"flame/tools/code"
)

// "The sandbox is busy" has to be a code of its own: it is a retryable transient rejection (the script never ran),
// and once folded back into script, on_error.retry.codes could never match it (see the note on scriptErrCode).
func TestScriptErrCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"busy", code.NewError(code.ErrCodeBusy, "sandbox busy: lua concurrency is at its limit of 32", nil), flow.ErrCodeBusy},
		{"busy (wrapped by fmt.Errorf)", fmt.Errorf("handler: %w",
			code.NewError(code.ErrCodeBusy, "sandbox busy", nil)), flow.ErrCodeBusy},
		{"script error", code.NewError(code.ErrCodeScriptError, "boom", nil), flow.ErrCodeScript},
		{"connect failure", code.NewError(code.ErrCodeConnectFail, "dial: refused", nil), flow.ErrCodeScript},
		{"not a script error", errors.New("code client not initialized"), flow.ErrCodeScript},
		// The snippet capability is not granted by the platform: it needs a code of its own so on_error does not
		// retry it as a script problem
		{"capability not granted", &capabilityError{capability: capability.Code}, flow.ErrCodeCapability},
		{"capability not granted (wrapped)", fmt.Errorf("handler: %w", &capabilityError{capability: capability.Code}), flow.ErrCodeCapability},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scriptErrCode(tc.err); got != tc.want {
				t.Errorf("scriptErrCode() = %q, want %q", got, tc.want)
			}
		})
	}
}
