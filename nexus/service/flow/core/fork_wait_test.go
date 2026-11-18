package flow

import (
	"context"
	"errors"
	"testing"
	"time"
)

// WaitTimeoutCtx 必须在 ctx 取消时立即返回：
// 用户停止工作流后，wait 节点不能挂到并发分支自己结束（timeout<=0 时尤其致命）。
func TestWaitTimeoutCtxCancelled(t *testing.T) {
	fr := NewForkResult(0)
	if err := fr.Add(1, context.Background(), ForkMember{ProcessID: "p1", NodeID: "n1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// 故意不 Done：模拟并发分支卡在不可中断的地方

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	completed, err := fr.WaitTimeoutCtx(ctx, -1)
	elapsed := time.Since(start)

	if completed {
		t.Fatal("未完成时应返回 completed=false")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v，want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("等待 %v 才返回，说明没有响应取消", elapsed)
	}
	if fr.State() != ForkStateAbandoned {
		t.Fatalf("state = %v，want Abandoned", fr.State())
	}
}

// 正常完成路径不受影响
func TestWaitTimeoutCtxCompleted(t *testing.T) {
	fr := NewForkResult(0)
	if err := fr.Add(1, context.Background(), ForkMember{ProcessID: "p1", NodeID: "n1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	go fr.Done()

	completed, err := fr.WaitTimeoutCtx(context.Background(), 5)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !completed {
		t.Fatal("应正常完成")
	}
	if fr.State() != ForkStateDone {
		t.Fatalf("state = %v，want Done", fr.State())
	}
}

// 超时仍按 timeout 返回 false，且 err 为 nil（与取消区分开）
func TestWaitTimeoutCtxTimeout(t *testing.T) {
	fr := NewForkResult(0)
	if err := fr.Add(1, context.Background(), ForkMember{ProcessID: "p1", NodeID: "n1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	completed, err := fr.WaitTimeoutCtx(context.Background(), 1)
	if err != nil {
		t.Fatalf("err = %v，want nil", err)
	}
	if completed {
		t.Fatal("超时应返回 completed=false")
	}
	if fr.State() != ForkStateAbandoned {
		t.Fatalf("state = %v，want Abandoned", fr.State())
	}
}
