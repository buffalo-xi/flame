package browserhost

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/go-json-experiment/json/jsontext"
)

// axVal 构造一个 AXValue（底层是原始 JSON）。
func axVal(s string) *accessibility.Value {
	return &accessibility.Value{Value: jsontext.Value(`"` + s + `"`)}
}

// 快照的三条硬约束：只给可操作节点发 ref、忽略节点不出现、纯文本可见但无 ref。
func TestRenderAX(t *testing.T) {
	nodes := []*accessibility.Node{
		{NodeID: "1", Role: axVal("RootWebArea"), Name: axVal("示例站点"),
			ChildIDs: []accessibility.NodeID{"2", "3", "4", "5"}},
		{NodeID: "2", ParentID: "1", Role: axVal("button"), Name: axVal("登录"), BackendDOMNodeID: 42},
		{NodeID: "3", ParentID: "1", Role: axVal("textbox"), Name: axVal("用户名"), BackendDOMNodeID: 43},
		// ignored 节点不该出现在快照里
		{NodeID: "4", ParentID: "1", Ignored: true, Role: axVal("generic"), Name: axVal("隐藏")},
		// 纯文本没有 ref，但要能看到
		{NodeID: "5", ParentID: "1", Role: axVal("StaticText"), Name: axVal("欢迎回来")},
	}

	text, refs, count, truncated := renderAX(nodes, 100, 4096)
	if truncated {
		t.Fatal("不该被截断")
	}
	// root + button + textbox + 文本 = 4 行
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
		t.Errorf("缺少 button 行:\n%s", text)
	}
	if strings.Contains(text, "隐藏") {
		t.Errorf("ignored 节点不该出现:\n%s", text)
	}
	if !strings.Contains(text, `[statictext] "欢迎回来"`) {
		t.Errorf("纯文本应保留:\n%s", text)
	}
	// 纯文本不该有 ref（否则 e 编号会被无意义的文本占满）
	if strings.Contains(text, "- e3 [statictext]") {
		t.Errorf("纯文本不该分配 ref:\n%s", text)
	}
}

// 预算截断必须明说 —— 静默截断会让模型以为「页面就这么多内容」。
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
		t.Fatal("超过 maxNodes 应标为截断")
	}
	if !strings.Contains(text, "truncated") {
		t.Errorf("截断提示缺失:\n%s", text)
	}
}
