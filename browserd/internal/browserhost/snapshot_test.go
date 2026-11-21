package browserhost

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/go-json-experiment/json/jsontext"
)

// axVal builds an AXValue (raw JSON underneath).
func axVal(s string) *accessibility.Value {
	return &accessibility.Value{Value: jsontext.Value(`"` + s + `"`)}
}

// The three hard constraints of a snapshot: refs go to operable nodes only, ignored nodes never appear, and plain
// text is visible but carries no ref.
func TestRenderAX(t *testing.T) {
	nodes := []*accessibility.Node{
		{NodeID: "1", Role: axVal("RootWebArea"), Name: axVal("示例站点"),
			ChildIDs: []accessibility.NodeID{"2", "3", "4", "5"}},
		{NodeID: "2", ParentID: "1", Role: axVal("button"), Name: axVal("登录"), BackendDOMNodeID: 42},
		{NodeID: "3", ParentID: "1", Role: axVal("textbox"), Name: axVal("用户名"), BackendDOMNodeID: 43},
		// An ignored node must not show up in the snapshot
		{NodeID: "4", ParentID: "1", Ignored: true, Role: axVal("generic"), Name: axVal("隐藏")},
		// Plain text has no ref, but must be visible
		{NodeID: "5", ParentID: "1", Role: axVal("StaticText"), Name: axVal("欢迎回来")},
	}

	text, refs, count, truncated := renderAX(nodes, 100, 4096)
	if truncated {
		t.Fatal("must not be truncated")
	}
	// root + button + textbox + text = 4 lines
	if count != 4 {
		t.Errorf("emitted = %d, want 4\n%s", count, text)
	}
	if refs["e1"] != 42 {
		t.Errorf("e1 = %v, want backendNodeId 42", refs["e1"])
	}
	if refs["e2"] != 43 {
		t.Errorf("e2 = %v, want backendNodeId 43", refs["e2"])
	}
	if !strings.Contains(text, `[button] "登录"`) {
		t.Errorf("missing the button line:\n%s", text)
	}
	if strings.Contains(text, "隐藏") {
		t.Errorf("an ignored node must not appear:\n%s", text)
	}
	if !strings.Contains(text, `[statictext] "欢迎回来"`) {
		t.Errorf("plain text must be kept:\n%s", text)
	}
	// Plain text must not get a ref (otherwise the e numbering would be filled with meaningless text)
	if strings.Contains(text, "- e3 [statictext]") {
		t.Errorf("plain text must not be assigned a ref:\n%s", text)
	}
}

// Budget truncation must be stated explicitly — silent truncation would make the model believe "this is all the
// page contains".
func TestRenderAXBudget(t *testing.T) {
	root := &accessibility.Node{NodeID: "1", Role: axVal("RootWebArea"), Name: axVal("root")}
	nodes := []*accessibility.Node{root}
	for i := 2; i < 40; i++ {
		id := accessibility.NodeID(fmt.Sprintf("n%d", i))
		root.ChildIDs = append(root.ChildIDs, id)
		nodes = append(nodes, &accessibility.Node{
			NodeID:           id,
			ParentID:         "1",
			Role:             axVal("button"),
			Name:             axVal("按钮"),
			BackendDOMNodeID: cdp.BackendNodeID(i),
		})
	}

	text, _, _, truncated := renderAX(nodes, 5, 4096)
	if !truncated {
		t.Fatal("exceeding maxNodes must be marked as truncated")
	}
	if !strings.Contains(text, "truncated") {
		t.Errorf("the truncation notice is missing:\n%s", text)
	}
}
