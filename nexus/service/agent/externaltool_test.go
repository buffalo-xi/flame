package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
)

// 连接名允许含下划线（toolgrant 的 namePattern），所以 mcp__ / rpc__ 的切分点
// 不能靠 "__" 盲切 —— 取最长匹配的连接名。这里把几种会出错的形状钉住。
func TestSplitExternalName(t *testing.T) {
	cases := []struct {
		name      string
		prefix    string
		connNames []string
		wantConn  string
		wantTool  string
		wantOK    bool
	}{
		{
			name: "mcp__fs__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantConn:  "fs", wantTool: "read_file", wantOK: true,
		},
		{
			// 连接名自身含下划线：按 "__" 盲切会拆成 ("fs", "prod__read_file")
			name: "mcp__fs__prod__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs", "fs__prod"},
			wantConn:  "fs__prod", wantTool: "read_file", wantOK: true,
		},
		{
			// 只有一个短连接时不越权猜测：切出来的工具名带 __ 也是对的
			name: "mcp__fs__prod__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantConn:  "fs", wantTool: "prod__read_file", wantOK: true,
		},
		{
			name: "rpc__erp__create_order", prefix: tools.ExternalPrefixRPC,
			connNames: []string{"erp", "crm"},
			wantConn:  "erp", wantTool: "create_order", wantOK: true,
		},
		{
			// 连接名大小写不敏感（与目录侧 EqualFold 同口径）
			name: "mcp__FS__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantConn:  "fs", wantTool: "read_file", wantOK: true,
		},
		{
			// 连接已被删除：解析不出来
			name: "mcp__gone__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantOK:    false,
		},
		{
			// 空工具名不是合法调用
			name: "mcp__fs__", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantOK:    false,
		},
		{
			// 前缀不对
			name: "rpc__fs__read_file", prefix: tools.ExternalPrefixMCP,
			connNames: []string{"fs"},
			wantOK:    false,
		},
	}

	for _, tc := range cases {
		conn, tool, ok := splitExternalName(tc.name, tc.prefix, tc.connNames)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if conn != tc.wantConn || tool != tc.wantTool {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, conn, tool, tc.wantConn, tc.wantTool)
		}
	}
}

// Has 只认 mcp__ / rpc__：local__ 归 localEndpointRunner，不能被误接管。
func TestExternalRunnerHas(t *testing.T) {
	r := &externalToolRunner{}
	for _, name := range []string{"mcp__fs__read_file", "rpc__erp__create_order"} {
		if !r.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"local__daily-report", "read_file", "custom__x", "web_search"} {
		if r.Has(name) {
			t.Errorf("Has(%q) = true, want false", name)
		}
	}
}

// is_error=true 是「远端业务错误」，不是调用异常：结果照返，模型才看得到错误正文。
func TestExternalResultKeepsBusinessError(t *testing.T) {
	got := externalResult("余额不足", nil, true, 0)
	if got["is_error"] != true {
		t.Errorf("is_error = %v, want true", got["is_error"])
	}
	if got["content"] != "余额不足" {
		t.Errorf("content = %v", got["content"])
	}
	if _, ok := got["truncated"]; ok {
		t.Error("未超限时不该带 truncated")
	}
}

// 超限必须截断并标记：外部工具吐整份文件时不能一次吃掉上下文。
func TestExternalResultTruncates(t *testing.T) {
	got := externalResult(strings.Repeat("a", 100), nil, false, 10)
	if got["content"] != strings.Repeat("a", 10) {
		t.Errorf("content 长度 = %d, want 10", len(got["content"].(string)))
	}
	if got["truncated"] != true {
		t.Error("超限时应带 truncated=true")
	}
}

// 截断不能切在 UTF-8 字符中间 —— 半个汉字在 JSON 里是坏字符。
func TestClampTextStopsAtRuneBoundary(t *testing.T) {
	// "汉字" = 6 字节；limit=4 落在第二个字中间
	got, truncated := clampText("汉字", 4)
	if !truncated {
		t.Fatal("应判定为截断")
	}
	if got != "汉" {
		t.Fatalf("got %q, want %q", got, "汉")
	}
}
