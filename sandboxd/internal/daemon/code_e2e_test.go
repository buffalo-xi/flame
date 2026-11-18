package daemon

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"flame/tools/sandbox"
	"flame/tools/code"
)

// 双工端点的端到端验收：真 sandboxd 服务 + 真 WebSocket + 真解释器（none 模式）。
//
// 验证的对象是本次 logs 改造的对外约定：脚本的 print 输出（成功路径）与
// traceback（失败路径）都要经 /v1/code 的 Frame.Logs 回到客户端。
//
// 需要本机跑着 sandboxd（缺省 127.0.0.1:27081，可用 SANDBOXD_TEST_CODE_URL 覆盖），
// 否则跳过。语言用 python（sandboxd 按配置解析入口名，客户端只传语言名）。

const defaultCodeURL = "ws://127.0.0.1:27081/v1/code"

// codeE2EForTest 探测 sandboxd 可达性：/healthz 不通就跳过。
func codeE2EForTest(t *testing.T) string {
	t.Helper()

	url := os.Getenv("SANDBOXD_TEST_CODE_URL")
	if url == "" {
		url = defaultCodeURL
	}
	base := strings.NewReplacer("ws://", "http://", "wss://", "https://").Replace(url)
	if i := strings.Index(base, "/v1/"); i > 0 {
		base = base[:i]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("sandboxd not reachable at %s (%v); set SANDBOXD_TEST_CODE_URL to enable", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("sandboxd /healthz returned %d at %s", resp.StatusCode, base)
	}
	return url
}

// TestScriptE2ELogsPassthrough 端到端验证 Frame.Logs 透传。
func TestScriptE2ELogsPassthrough(t *testing.T) {
	url := codeE2EForTest(t)
	c := code.NewClient(url, "", sandbox.Limits{})

	t.Run("success logs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		r, err := c.Execute(ctx, "python",
			"print('hello-logs')\n__return__ = 'ok'\n", nil, 30)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if r.Value != "ok" {
			t.Fatalf("result = %v (%T), want ok", r.Value, r.Value)
		}
		if !strings.Contains(r.Logs, "hello-logs") {
			t.Fatalf("logs = %q, want contain hello-logs", r.Logs)
		}
	})

	t.Run("error logs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err := c.Execute(ctx, "python", "raise ValueError('boom-logs')\n", nil, 30)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		se, ok := err.(*code.Error)
		if !ok {
			t.Fatalf("err type = %T, want *code.Error", err)
		}
		if !strings.Contains(se.Message, "boom-logs") {
			t.Fatalf("message = %q, want contain boom-logs", se.Message)
		}
		if !strings.Contains(se.Logs, "Traceback") {
			t.Fatalf("logs = %q, want contain Traceback", se.Logs)
		}
	})
}
