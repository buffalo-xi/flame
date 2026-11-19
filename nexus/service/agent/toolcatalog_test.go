package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/toolgrant"
)

func leaf(ref, name string, available bool) toolgrant.ToolInfo {
	return toolgrant.ToolInfo{Ref: ref, Name: name, Label: name, Available: available}
}

// 空 query 只给组概览：工具可能有上百个，全倒进上下文既没用又挤占窗口。
func TestSearchCatalogEmptyQueryReturnsGroupsOnly(t *testing.T) {
	all := map[string]toolgrant.ToolInfo{
		"builtin:read_file": leaf("builtin:read_file", "read_file", true),
	}
	groups := []toolgrant.GroupInfo{{
		Ref: "builtin:file", Name: "内置-file", Kind: "builtin", Available: true,
		Items: []toolgrant.ToolInfo{leaf("builtin:read_file", "read_file", true)},
	}}

	res := searchCatalog("", all, groups, nil)
	if len(res.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(res.Groups))
	}
	if len(res.Tools) != 0 {
		t.Fatalf("空 query 不该展开工具，实际 %d 条", len(res.Tools))
	}
	if len(res.Notes) == 0 {
		t.Fatal("应提示「未给 query 时只列组概览」")
	}
	if res.Groups[0].Count != 1 {
		t.Errorf("Count = %d, want 1", res.Groups[0].Count)
	}
}

// 合成容器（plugin:* / custom:*）不是可授权对象，必须被剔掉：
// 它的 ref 会被 ValidateRef 拒，成员各自以工具 ref 出现在下面。
func TestSearchCatalogSkipsSyntheticGroups(t *testing.T) {
	groups := []toolgrant.GroupInfo{
		{Ref: "custom:*", Name: "自定义工具", Kind: "script", Available: true, Synthetic: true},
		{Ref: "builtin:file", Name: "内置-file", Kind: "builtin", Available: true},
	}
	res := searchCatalog("", nil, groups, nil)
	if len(res.Groups) != 1 || res.Groups[0].Ref != "builtin:file" {
		t.Fatalf("groups = %+v", res.Groups)
	}
}

// 给了 query 才展开到工具，且组与工具都按关键字匹配。
func TestSearchCatalogQueryExpandsTools(t *testing.T) {
	all := map[string]toolgrant.ToolInfo{
		"mcp:fs:read_file":  leaf("mcp:fs:read_file", "mcp__fs__read_file", true),
		"mcp:fs:write_file": leaf("mcp:fs:write_file", "mcp__fs__write_file", true),
		"builtin:read_file": leaf("builtin:read_file", "read_file", true),
	}
	groups := []toolgrant.GroupInfo{
		{Ref: "mcp:fs", Name: "fs", Kind: "mcp", Available: true},
		{Ref: "builtin:file", Name: "内置-file", Kind: "builtin", Available: true},
	}

	res := searchCatalog("fs", all, groups, nil)
	if len(res.Groups) != 1 || res.Groups[0].Ref != "mcp:fs" {
		t.Fatalf("groups = %+v", res.Groups)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("tools = %d, want 2（mcp:fs 下两个工具）", len(res.Tools))
	}
	for _, it := range res.Tools {
		if !strings.HasPrefix(it.Ref, "mcp:fs:") {
			t.Errorf("命中了不该匹配的工具：%s", it.Ref)
		}
		if it.Kind != "mcp" {
			t.Errorf("%s kind = %q, want mcp", it.Ref, it.Kind)
		}
	}

	// 跨 scope 的工具名匹配：mcp 与 builtin 各一条
	res = searchCatalog("read_file", all, groups, nil)
	if len(res.Tools) != 2 {
		t.Fatalf("read_file 命中 %d 条，want 2", len(res.Tools))
	}
}

// 超限必须截断并提示：否则一次倒出上百个工具会挤爆上下文。
func TestSearchCatalogTruncates(t *testing.T) {
	all := make(map[string]toolgrant.ToolInfo, 25)
	for i := 0; i < 25; i++ {
		ref := "mcp:fs:tool" + string(rune('a'+i))
		all[ref] = leaf(ref, ref, true)
	}
	res := searchCatalog("mcp:fs", all, nil, nil)
	if len(res.Tools) != toolSearchLimit {
		t.Fatalf("tools = %d, want %d", len(res.Tools), toolSearchLimit)
	}
	if !res.Truncated {
		t.Fatal("应标记 Truncated")
	}
	if len(res.Notes) == 0 {
		t.Fatal("截断时应提示收窄 query")
	}
}

// 可用优先 + 同状态按 ref 字典序：保证同样输入每次输出一致
// （map 迭代无序，不排序会让模型以为目录变了）。
func TestSearchCatalogSortsAvailableFirstAndStable(t *testing.T) {
	groups := []toolgrant.GroupInfo{
		{Ref: "builtin:zzz", Name: "zzz", Kind: "builtin", Available: true},
		{Ref: "builtin:aaa", Name: "aaa", Kind: "builtin", Available: false},
		{Ref: "builtin:mmm", Name: "mmm", Kind: "builtin", Available: true},
	}
	res := searchCatalog("", nil, groups, nil)
	got := []string{res.Groups[0].Ref, res.Groups[1].Ref, res.Groups[2].Ref}
	want := []string{"builtin:mmm", "builtin:zzz", "builtin:aaa"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序 = %v, want %v", got, want)
		}
	}
}

// 传了目标 agent 时回填已授权状态：避免重复授权，也让「授过什么」可见。
func TestSearchCatalogMarksGranted(t *testing.T) {
	groups := []toolgrant.GroupInfo{{Ref: "mcp:fs", Name: "fs", Kind: "mcp", Available: true}}
	all := map[string]toolgrant.ToolInfo{
		"mcp:fs:read_file": leaf("mcp:fs:read_file", "mcp__fs__read_file", true),
	}
	granted := map[string]string{"mcp:fs:read_file": "required"}

	res := searchCatalog("fs", all, groups, granted)
	if len(res.Tools) != 1 || res.Tools[0].Granted != "required" {
		t.Fatalf("tools = %+v", res.Tools)
	}
	if res.Groups[0].Granted != "" {
		t.Errorf("未授权的组不该带 Granted：%+v", res.Groups[0])
	}
}

// 组卡片必须带成员摘要：授权是对 ref 的操作，而「这个组里到底有什么」既不在工具名里
// 也不在描述里 —— 不给摘要，模型只能盲授整组。
func TestSearchCatalogGroupMembers(t *testing.T) {
	groups := []toolgrant.GroupInfo{{
		Ref: "group:fs-basic", Name: "fs-basic", Kind: "group", Available: true,
		Items: []toolgrant.ToolInfo{
			leaf("builtin:read_file", "read_file", true),
			leaf("builtin:write_file", "write_file", true),
		},
	}}
	res := searchCatalog("", nil, groups, nil)
	got := res.Groups[0]
	if len(got.Members) != 2 {
		t.Fatalf("members = %v, want 2 条", got.Members)
	}
	if got.Members[0] != "builtin:read_file" {
		t.Errorf("members[0] = %q, want builtin:read_file（应与 Items 同序）", got.Members[0])
	}
	if got.Count != 2 {
		t.Errorf("count = %d, want 2", got.Count)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "只回了前") {
			t.Errorf("未超限不该提示截断：%q", n)
		}
	}
}

// 成员数超限时截断，但 count 保持真实总数，并明确提示怎么拿全量。
func TestSearchCatalogTruncatesGroupMembers(t *testing.T) {
	items := make([]toolgrant.ToolInfo, 0, groupMemberPreview+3)
	for i := 0; i < groupMemberPreview+3; i++ {
		ref := "builtin:tool" + string(rune('a'+i))
		items = append(items, leaf(ref, ref, true))
	}
	groups := []toolgrant.GroupInfo{{
		Ref: "group:big", Name: "big", Kind: "group", Available: true, Items: items,
	}}
	res := searchCatalog("", nil, groups, nil)
	got := res.Groups[0]
	if len(got.Members) != groupMemberPreview {
		t.Fatalf("members = %d, want %d", len(got.Members), groupMemberPreview)
	}
	if got.Count != len(items) {
		t.Errorf("count = %d, want %d（截断只影响摘要，不影响总数）", got.Count, len(items))
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "group:big") && strings.Contains(n, "只回了前") {
			found = true
		}
	}
	if !found {
		t.Errorf("应提示 group:big 成员被截断，notes = %v", res.Notes)
	}
}

// 零命中要明确说出来并给跨语言退路：matchQuery 是子串匹配，中文 query 撞上英文目录
// （或反过来）会一条都不命中；静默返回空容易被读成「本租户没有这类工具」。
func TestSearchCatalogZeroHitNote(t *testing.T) {
	all := map[string]toolgrant.ToolInfo{
		"builtin:read_file": leaf("builtin:read_file", "read_file", true),
	}
	res := searchCatalog("搜索文件", all, nil, nil)
	if len(res.Groups) != 0 || len(res.Tools) != 0 {
		t.Fatalf("不该命中：groups=%d tools=%d", len(res.Groups), len(res.Tools))
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "没有匹配项") && strings.Contains(n, "substring") {
			found = true
		}
	}
	if !found {
		t.Errorf("零命中时应提示跨语言子串匹配的限制，notes = %v", res.Notes)
	}

	// 有命中时不能加这条噪音
	res = searchCatalog("read_file", all, nil, nil)
	for _, n := range res.Notes {
		if strings.Contains(n, "没有匹配项") {
			t.Errorf("有命中时不该提示零命中：%q", n)
		}
	}
}

// 没有注入目录管理器时必须是明确错误，而不是空结果（空结果会让模型以为"没有工具"）。
// 工具本体在 tools 包，这里只验 handler 这一侧的接线，见 tools/search_tools_test.go。
