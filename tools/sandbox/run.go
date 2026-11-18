package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

// Run 一次性执行的便捷封装：
//
//	Start → 写入 Spec.Stdin（若有）→ 关闭 stdin → 收集 stdout/stderr → Wait
//
// 它不改动传入的 spec（内部复制一份），并把 stderr 收进 Result。
// 输出受 Limits.MaxOutputBytes 约束（超出即截断并标记 Truncated），
// 避免大输出吃爆内存。长驻交互场景不要用它 —— 直接用 Sandbox.Start。
func Run(ctx context.Context, sb Sandbox, spec *Spec) (*Result, error) {
	if sb == nil {
		return nil, errors.New("sandbox: nil sandbox")
	}

	stderrOut := newLimitedWriter(spec.Limits.MaxOutputBytes)
	local := *spec // 复制，避免污染调用方
	local.Stderr = stderrOut
	local.Stdin = nil // Stdin 字段仅由 Run 消费，不回传给实现

	proc, err := sb.Start(ctx, &local)
	if err != nil {
		return nil, err
	}

	// 写输入并关闭 stdin（表示"发送结束"）
	if len(spec.Stdin) > 0 {
		if _, werr := proc.Stdin().Write(spec.Stdin); werr != nil {
			_ = proc.Kill()
			return nil, werr
		}
	}
	_ = proc.Stdin().Close()

	// 并发读 stdout（与 stderr 写入同步进行，避免管道写满阻塞子进程）
	stdoutOut := newLimitedWriter(spec.Limits.MaxOutputBytes)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stdoutOut, proc.Stdout())
	}()

	waitErr := proc.Wait()
	wg.Wait()

	res := &Result{
		Stdout:    stdoutOut.Bytes(),
		Stderr:    stderrOut.Bytes(),
		Truncated: stdoutOut.Truncated() || stderrOut.Truncated(),
	}
	if waitErr != nil {
		var ee *ExitError
		if errors.As(waitErr, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil // 非零退出码不算错误，交给调用方判断
		}
		return res, waitErr
	}
	return res, nil
}

// limitedWriter 带上限的缓冲区：达到上限后继续接收但丢弃内容，
// 并置 Truncated。始终返回 len(p) 是为了不阻塞写入方（子进程）。
type limitedWriter struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func newLimitedWriter(max int64) *limitedWriter {
	return &limitedWriter{max: max}
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.max <= 0 {
		return w.buf.Write(p)
	}
	remain := w.max - int64(w.buf.Len())
	if remain <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remain {
		_, _ = w.buf.Write(p[:remain])
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *limitedWriter) Bytes() []byte   { return w.buf.Bytes() }
func (w *limitedWriter) Truncated() bool { return w.truncated }
