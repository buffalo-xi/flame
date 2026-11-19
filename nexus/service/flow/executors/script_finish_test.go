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
		{"unsupported language", code.NewError(code.ErrCodeUnsupportedLang, "no lua", nil), flow.ErrCodeConfig},
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
