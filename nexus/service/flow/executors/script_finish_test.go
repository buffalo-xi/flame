package executors

import (
	"context"
	"errors"
	"testing"
	"time"

	"flame/nexus/service/flow/core"
	"flame/tools/code"
)

// The three script-like nodes (code / result / progress) share one error-code mapping: the same class of error
// has to get the same code in whichever node it happens, otherwise a retry/skip configured by code in on_error
// stops working depending on the node type.
func TestFinishScriptErrorCodes(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "code"}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"timeout", code.NewError(code.ErrCodeTimeout, "script timeout", nil), flow.ErrCodeTimeout},
		{"busy", code.NewError(code.ErrCodeBusy, "sandbox busy", nil), flow.ErrCodeBusy},
		{"script error", code.NewError(code.ErrCodeScriptError, "boom", nil), flow.ErrCodeScript},
		// A connection failure is classified as connection rather than internal: internal is returned outright by
		// shouldBypassOnError, which would lock down the class of error that most deserves a retry
		{"connect failure", code.NewError(code.ErrCodeConnectFail, "dial refused", nil), flow.ErrCodeConnection},
		{"worker communication", code.NewError(code.ErrCodeWorkerComm, "worker gone", nil), flow.ErrCodeConnection},
		{"send failure", code.NewError(code.ErrCodeSendFail, "send failed", nil), flow.ErrCodeConnection},
		{"unsupported language", code.NewError(code.ErrCodeUnsupportedLang, "no lua", nil), flow.ErrCodeConfig},
		// The sandbox answered with an action the protocol does not know: a platform-side defect, but still
		// classified as other so on_error can see it
		{"unknown action", code.NewError(code.ErrCodeUnknownAction, "?", nil), flow.ErrCodeOther},
		{"not a script error", errors.New("code client not initialized"), flow.ErrCodeInternal},
		{"capability not granted", &capabilityError{capability: "code"}, flow.ErrCodeCapability},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &flow.ExecContext{StdCtx: context.Background()}
			nr := flow.NewNodeResult(node, "1", ctx)

			got := finishScriptError(ctx, nr, tc.err)

			var ee *ExecError
			if !errors.As(got, &ee) {
				t.Fatalf("the return value should be *ExecError (otherwise shouldBypassOnError cannot read the code), got %T", got)
			}
			if ee.Code != tc.want {
				t.Errorf("code = %q, want %q", ee.Code, tc.want)
			}
			if nr.Status != flow.StatusFailed {
				t.Errorf("status = %v, want Failed", nr.Status)
			}
		})
	}
}

// Cancellation errors must be split by ctx into "the user stopped it" and "the workflow timed out", never by
// matching the message text.
// A target without source_key writes to the key the step returns. The field used to be passed through empty, which reached
// SetOutputVar("") and failed with "output var  not defined" - an error that named nothing.
func TestTargetSourceKeyFallsBackToReturnedKey(t *testing.T) {
	cases := []struct {
		name        string
		def         map[string]any
		returnedKey string
		want        string
	}{
		{"an explicit source_key wins", map[string]any{"source_key": "total"}, "sum", "total"},
		{"a missing source_key uses the returned key", map[string]any{}, "sum", "sum"},
		{"a returned sub-path is dropped", map[string]any{}, "sum.value", "sum"},
		{"an array sub-path is dropped", map[string]any{}, "items[0]", "items"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := targetSourceKey(tc.def, tc.returnedKey); got != tc.want {
				t.Errorf("targetSourceKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFinishScriptErrorCancelVsTimeout(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "code"}

	t.Run("user cancelled", func(t *testing.T) {
		c, cancel := context.WithCancel(context.Background())
		cancel()
		ctx := &flow.ExecContext{StdCtx: c}
		nr := flow.NewNodeResult(node, "1", ctx)

		got := finishScriptError(ctx, nr, code.NewError(code.ErrCodeCancel, "context canceled", nil))

		var ee *ExecError
		if !errors.As(got, &ee) || ee.Code != flow.ErrCodeCancel {
			t.Fatalf("code = %v, want %q", got, flow.ErrCodeCancel)
		}
		if nr.Status != flow.StatusCancelled {
			t.Fatalf("status = %v, want Cancelled", nr.Status)
		}
	})

	t.Run("workflow timed out", func(t *testing.T) {
		c, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		ctx := &flow.ExecContext{StdCtx: c}
		nr := flow.NewNodeResult(node, "1", ctx)

		got := finishScriptError(ctx, nr, code.NewError(code.ErrCodeCancel, "context deadline exceeded", nil))

		var ee *ExecError
		if !errors.As(got, &ee) || ee.Code != flow.ErrCodeTimeout {
			t.Fatalf("code = %v, want %q (a timeout must not be classified as cancel)", got, flow.ErrCodeTimeout)
		}
	})
}
