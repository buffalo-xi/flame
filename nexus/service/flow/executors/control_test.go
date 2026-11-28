package executors

import (
	"errors"
	"fmt"
	"testing"

	"flame/nexus/service/flow/core"
)

// Control signals travel through the error interface but are normal control flow: each executor succeeds its
// own node and hands the signal to the enclosing loop / function / flow.
func TestControlSignalExecutors(t *testing.T) {
	cases := []struct {
		name     string
		exec     Executor
		signal   error
		nodeType string
	}{
		{"break", &BreakExecutor{}, SignalBreak, "break"},
		{"continue", &ContinueExecutor{}, SignalContinue, "continue"},
		{"return", &ReturnExecutor{}, SignalReturn, "return"},
		{"exit", &ExitExecutor{}, SignalExit, "exit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", tc.nodeType, map[string]any{})
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			err := tc.exec.Execute(node, ctx, "1", nr)
			if !errors.Is(err, tc.signal) {
				t.Fatalf("Execute = %v, want the control signal", err)
			}
			if !isControlSignal(err) {
				t.Error("isControlSignal does not recognise the signal: the runner would treat it as a failure")
			}
			if nr.Status != flow.StatusSuccess {
				t.Errorf("status = %v, want success", nr.Status)
			}
			if nr.ErrCode != "" {
				t.Errorf("ErrCode = %q, want empty", nr.ErrCode)
			}
		})
	}
}

func TestIsControlSignal(t *testing.T) {
	if isControlSignal(nil) {
		t.Error("nil is not a control signal")
	}
	if isControlSignal(errors.New("boom")) {
		t.Error("a plain error is not a control signal")
	}
	// Wrapped signals still count: containers add context layer by layer.
	if !isControlSignal(fmt.Errorf("loop body: %w", SignalBreak)) {
		t.Error("a wrapped signal was not recognised")
	}
}

// parseTimeoutConfig is shared by branch / loop / code / result / progress / wait / http, so the "off" cases are
// the contract: an absent or disabled block must mean "no node-level timeout", never 0 seconds.
func TestParseTimeoutConfig(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want int
	}{
		{"absent", nil, -1},
		{"not an object", "5", -1},
		{"disabled", map[string]any{"enabled": false, "seconds": float64(5)}, -1},
		{"enabled without seconds", map[string]any{"enabled": true}, -1},
		{"zero seconds", map[string]any{"enabled": true, "seconds": float64(0)}, -1},
		{"negative seconds", map[string]any{"enabled": true, "seconds": float64(-3)}, -1},
		{"string seconds", map[string]any{"enabled": true, "seconds": "5"}, -1},
		{"float seconds", map[string]any{"enabled": true, "seconds": float64(5)}, 5},
		{"int seconds", map[string]any{"enabled": true, "seconds": 7}, 7},
		{"int64 seconds", map[string]any{"enabled": true, "seconds": int64(9)}, 9},
		// The result is whole seconds: a fractional value is truncated, not rounded up.
		{"fractional seconds", map[string]any{"enabled": true, "seconds": float64(1.9)}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTimeoutConfig(tc.raw); got != tc.want {
				t.Errorf("parseTimeoutConfig(%#v) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}