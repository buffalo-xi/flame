package sandbox

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"os"
)

// 手动冒烟：真实站点（百度）走完整链路 —— 打开 → 快照 → 输入随机词 → 搜索。
//
// 门控：SANDBOXD_TEST_BROWSER_URL=ws://127.0.0.1:27080/v1/interactive
// 只适合 mode=none（浏览器与测试进程同一台机器、能互访回环）。
func TestBaiduSearch(t *testing.T) {
	ep := os.Getenv("SANDBOXD_TEST_BROWSER_URL")
	if ep == "" {
		t.Skip("set SANDBOXD_TEST_BROWSER_URL to run")
	}

	// 必须落在 sandboxd 的 allowed_mount_roots 之内（本机是 nexus/data 之下）。
	wsDir := os.Getenv("BROWSER_TEST_WORKSPACE")
	if wsDir == "" {
		t.Skip("set BROWSER_TEST_WORKSPACE to a dir under sandboxd's allowed_mount_roots")
	}
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Logf("workspace: %s", wsDir)

	c := NewInteractive(ep, "", Limits{})
	sess := InteractiveSession{Key: fmt.Sprintf("baidu-%d", time.Now().UnixNano()), Ephemeral: true}
	mounts := []Mount{{Host: wsDir, Guest: "/workspace"}}
	defer func() { _ = c.Kill(context.Background(), sess, "browser") }()

	do := func(id int, req map[string]any) map[string]any {
		t.Helper()
		req["id"] = id
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		line, err := c.Do(ctx, sess, "browser", []string{"browserhost"}, mustJSON(t, req), mounts, "/workspace")
		if err != nil {
			t.Fatalf("op %v: %v", req["op"], err)
		}
		res := decode(t, line)
		if ok, _ := res["ok"].(bool); !ok {
			t.Fatalf("op %v failed: %s", req["op"], line)
		}
		return res
	}

	headed := os.Getenv("BROWSER_TEST_HEADED") == "1"
	open := do(1, map[string]any{"op": "open", "url": "https://www.baidu.com", "headed": headed})
	t.Logf("open → url=%v title=%v", open["url"], open["title"])
	t.Logf("snapshot:\n%s", open["snapshot"])

	snap, _ := open["snapshot"].(string)
	ref := refOfRole(snap, "textbox", "searchbox", "combobox")
	if ref == "" {
		t.Fatalf("页面里找不到输入框，快照:\n%s", snap)
	}

	query := fmt.Sprintf("flame%d", rand.Intn(90000)+10000)
	t.Logf("query: %s (ref=%s)", query, ref)

	typed := do(2, map[string]any{"op": "type", "ref": ref, "text": query, "submit": true})
	t.Logf("type → url=%v", typed["url"])

	// 搜索是否真的提交：看地址栏里有没有 wd=<query>。
	// 这一步与「百度让不让人看结果」是两件事 —— 触发反爬验证不算工具失败。
	typedURL, _ := typed["url"].(string)
	if !strings.Contains(typedURL, "wd="+query) && !strings.Contains(typedURL, "word="+query) {
		t.Errorf("搜索没有提交: %s", typedURL)
	}

	// 结果区是 JS 渲染的：刚跳过去时只有骨架屏（innerText 几乎是空的），
	// 等 #content_left 里真的有结果块再读，否则会误判成「页面没内容」。
	do(3, map[string]any{"op": "wait", "selector": "#content_left .c-container", "timeout": 20})

	typeRes := do(4, map[string]any{"op": "text", "max_bytes": 3000})
	url, _ := typeRes["url"].(string)
	body, _ := typeRes["text"].(string)
	t.Logf("result → url=%v", url)
	t.Logf("text(前 400 字):\n%.400s", body)

	switch {
	case strings.Contains(body, "百度安全验证"):
		t.Logf("百度触发了反爬验证（滑块），这是站点行为，不是工具问题")
	case body == "":
		t.Errorf("结果页正文为空（渲染未完成或取文本失败）")
	case !strings.Contains(body, query):
		t.Logf("正文里没有查询词（随机串本就可能无结果），正文长度 %d", len(body))
	}

	shot := do(5, map[string]any{"op": "screenshot", "path": "baidu-search.png"})
	t.Logf("screenshot → %v (%v bytes)", shot["path"], shot["bytes"])
}

// refOfRole 从快照里挑第一个指定角色的 ref（形如 `- e3 [textbox] "百度一下"`）。
func refOfRole(snapshot string, roles ...string) string {
	for _, line := range strings.Split(snapshot, "\n") {
		for _, role := range roles {
			if !strings.Contains(line, "["+role+"]") {
				continue
			}
			for _, field := range strings.Fields(line) {
				if len(field) > 1 && field[0] == 'e' && field[1] >= '0' && field[1] <= '9' {
					return field
				}
			}
		}
	}
	return ""
}
