package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Cause 是唯一可能携带内网地址的字段（如 "get http://127.0.0.1:27081/...: http 500"），
// 而 /health 会把 readinessIssue 整个序列化给探针。json:"-" 就是那道闸 ——
// 它要是被顺手改掉，内网拓扑会随探针响应外泄，且没有任何别的地方会因此变红。
func TestReadinessIssueCauseNotSerialized(t *testing.T) {
	issue := readinessIssue{
		Key:    "sandbox",
		Detail: "sandbox capability snapshot not fetched yet",
		Cause:  "get http://127.0.0.1:27081/v1/capabilities: http 500",
	}
	b, err := json.Marshal([]readinessIssue{issue})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "127.0.0.1") || strings.Contains(string(b), "capabilities") {
		t.Fatalf("Cause 不该进响应体, got %s", b)
	}

	// 探针仍要能看出「是哪条依赖挂了」—— 压掉 Cause 不能连 Key/Detail 一起压掉。
	var back []readinessIssue
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 1 || back[0].Key != "sandbox" || back[0].Detail == "" {
		t.Errorf("探针该拿到 dependency/detail, got %+v", back)
	}
}

// describeIssues 是日志面：必须带上 Cause，否则排障时又回到「不知道哪条、为什么挂」。
func TestDescribeIssues(t *testing.T) {
	got := describeIssues([]readinessIssue{
		{Key: "db", Detail: "database not connected"},
		{Key: "sandbox", Detail: "snapshot not fetched yet", Cause: "http 500"},
	})
	want := "db: database not connected; sandbox: snapshot not fetched yet (http 500)"
	if got != want {
		t.Errorf("describeIssues = %q, want %q", got, want)
	}
	if got := describeIssues(nil); got != "" {
		t.Errorf("就绪时不该产出文案, got %q", got)
	}
}
