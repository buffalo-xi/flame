package executors

import (
	"context"
	"errors"
	"testing"
	"time"

	"flame/nexus/service/flow/core"
	"flame/tools/code"
)

// 三个脚本类节点（code / result / progress）共用同一份错误码映射：
// 同一类错误必须在哪个节点都归到同一个码，否则 on_error 里按码配的重试/跳过会因节点类型而失效。
func TestFinishScriptErrorCodes(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "code"}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"超时", code.NewError(code.ErrCodeTimeout, "script timeout", nil), flow.ErrCodeTimeout},
		{"繁忙", code.NewError(code.ErrCodeBusy, "sandbox busy", nil), flow.ErrCodeBusy},
		{"脚本报错", code.NewError(code.ErrCodeScriptError, "boom", nil), flow.ErrCodeScript},
		// 连接失败归 connection 而不是 internal：internal 会被 shouldBypassOnError 直接返回，
		// 等于把最该重试的一类错误锁死
		{"连接失败", code.NewError(code.ErrCodeConnectFail, "dial refused", nil), flow.ErrCodeConnection},
		{"不支持的语言", code.NewError(code.ErrCodeUnsupportedLang, "no lua", nil), flow.ErrCodeConfig},
		{"非脚本错误", errors.New("code client not initialized"), flow.ErrCodeInternal},
		{"能力未开通", &capabilityError{capability: "code"}, flow.ErrCodeCapability},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &flow.ExecContext{StdCtx: context.Background()}
			nr := flow.NewNodeResult(node, "1", ctx)

			got := finishScriptError(ctx, nr, tc.err)

			var ee *ExecError
			if !errors.As(got, &ee) {
				t.Fatalf("返回值应是 *ExecError（否则 shouldBypassOnError 读不到码），got %T", got)
			}
			if ee.Code != tc.want {
				t.Errorf("code = %q，want %q", ee.Code, tc.want)
			}
			if nr.Status != flow.StatusFailed {
				t.Errorf("status = %v，want Failed", nr.Status)
			}
		})
	}
}

// 取消类错误要按 ctx 区分「用户停止」与「工作流超时」，不能靠匹配错误文本。
func TestFinishScriptErrorCancelVsTimeout(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "code"}

	t.Run("用户取消", func(t *testing.T) {
		c, cancel := context.WithCancel(context.Background())
		cancel()
		ctx := &flow.ExecContext{StdCtx: c}
		nr := flow.NewNodeResult(node, "1", ctx)

		got := finishScriptError(ctx, nr, code.NewError(code.ErrCodeCancel, "context canceled", nil))

		var ee *ExecError
		if !errors.As(got, &ee) || ee.Code != flow.ErrCodeCancel {
			t.Fatalf("code = %v，want %q", got, flow.ErrCodeCancel)
		}
		if nr.Status != flow.StatusCancelled {
			t.Fatalf("status = %v，want Cancelled", nr.Status)
		}
	})

	t.Run("工作流超时", func(t *testing.T) {
		c, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		ctx := &flow.ExecContext{StdCtx: c}
		nr := flow.NewNodeResult(node, "1", ctx)

		got := finishScriptError(ctx, nr, code.NewError(code.ErrCodeCancel, "context deadline exceeded", nil))

		var ee *ExecError
		if !errors.As(got, &ee) || ee.Code != flow.ErrCodeTimeout {
			t.Fatalf("code = %v，want %q（超时不能被归成 cancel）", got, flow.ErrCodeTimeout)
		}
	})
}
