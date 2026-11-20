package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/toolgrant"
)

func leaf(ref, name string, available bool) toolgrant.ToolInfo {
	return toolgrant.ToolInfo{Ref: ref, Name: name, Label: name, Available: available}
}

// An empty query gives the group overview only: tools may number in the hundreds, and dumping all of them into the context is useless and eats
// the window.
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
		t.Fatalf("an empty query must not expand tools, got %d", len(res.Tools))
	}
	if len(res.Notes) == 0 {
		t.Fatal("should note that without a query only the group overview is listed")
	}
	if res.Groups[0].Count != 1 {
		t.Errorf("Count = %d, want 1", res.Groups[0].Count)
	}
}

// Synthetic containers (plugin:* / custom:*) are not grantable and must be dropped:
// ValidateRef refuses their ref, and their members appear below as individual tool refs.
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

// Tools are expanded only when a query is given, and both groups and tools match on the keyword.
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
		t.Fatalf("tools = %d, want 2 (the two tools under mcp:fs)", len(res.Tools))
	}
	for _, it := range res.Tools {
		if !strings.HasPrefix(it.Ref, "mcp:fs:") {
			t.Errorf("matched a tool that should not match: %s", it.Ref)
		}
		if it.Kind != "mcp" {
			t.Errorf("%s kind = %q, want mcp", it.Ref, it.Kind)
		}
	}

	// A tool-name match across scopes: one from mcp, one from builtin
	res = searchCatalog("read_file", all, groups, nil)
	if len(res.Tools) != 2 {
		t.Fatalf("read_file matched %d, want 2", len(res.Tools))
	}
}

// Over the limit it must truncate and say so: a hundred tools dumped in one go would blow up the context.
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
		t.Fatal("Truncated must be set")
	}
	if len(res.Notes) == 0 {
		t.Fatal("truncation should ask for a narrower query")
	}
}

// Available first, then by ref lexicographically: the same input must always produce the same output
// (map iteration is unordered, and without sorting the model thinks the catalog changed).
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
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// With a target agent the granted state is filled in: it avoids duplicate grants and makes "what was granted" visible.
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
		t.Errorf("an ungranted group must not carry Granted: %+v", res.Groups[0])
	}
}

// A group card must carry a member digest: granting works on refs, and "what is actually in this group" is in neither the tool name nor the
// description - without a digest the model can only grant the whole group blindly.
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
		t.Fatalf("members = %v, want 2", got.Members)
	}
	if got.Members[0] != "builtin:read_file" {
		t.Errorf("members[0] = %q, want builtin:read_file (the same order as Items)", got.Members[0])
	}
	if got.Count != 2 {
		t.Errorf("count = %d, want 2", got.Count)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "only the first") {
			t.Errorf("no truncation note should appear under the limit: %q", n)
		}
	}
}

// Over the member limit the digest truncates, but count keeps the real total and a note says how to fetch everything.
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
		t.Errorf("count = %d, want %d (truncation affects the digest, not the total)", got.Count, len(items))
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "group:big") && strings.Contains(n, "only the first") {
			found = true
		}
	}
	if !found {
		t.Errorf("the truncation of group:big members should be noted, notes = %v", res.Notes)
	}
}

// Zero hits must be stated outright with a cross-language way out: matchQuery is a substring match, so a Chinese query against an English
// catalog (or the reverse) hits nothing; returning empty silently reads as "this tenant has no such tool".
func TestSearchCatalogZeroHitNote(t *testing.T) {
	all := map[string]toolgrant.ToolInfo{
		"builtin:read_file": leaf("builtin:read_file", "read_file", true),
	}
	res := searchCatalog("搜索文件", all, nil, nil)
	if len(res.Groups) != 0 || len(res.Tools) != 0 {
		t.Fatalf("should not match anything: groups=%d tools=%d", len(res.Groups), len(res.Tools))
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "No match") && strings.Contains(n, "substring") {
			found = true
		}
	}
	if !found {
		t.Errorf("zero hits should explain the cross-language substring limitation, notes = %v", res.Notes)
	}

	// No such noise when something matched
	res = searchCatalog("read_file", all, nil, nil)
	for _, n := range res.Notes {
		if strings.Contains(n, "No match") {
			t.Errorf("a zero-hit note must not appear when something matched: %q", n)
		}
	}
}

// Without an injected catalog manager it must be an explicit error rather than an empty result (an empty result makes the model think "there are
// no tools"). The tool itself lives in the tools package; this only covers the wiring on the handler side, see tools/search_tools_test.go.
