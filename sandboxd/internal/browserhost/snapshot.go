package browserhost

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
)

// interactiveRoles 会被分配 ref 的角色。
//
// 只有「能操作的东西」才值得一个 ref：给每个文本节点也编号会把快照撑成
// 一堆无意义的 e17/e18，模型反而更难找目标。纯文本节点照常输出，只是没有 ref。
var interactiveRoles = map[string]bool{
	"button":           true,
	"link":             true,
	"textbox":          true,
	"searchbox":        true,
	"checkbox":         true,
	"radio":            true,
	"combobox":         true,
	"listbox":          true,
	"option":           true,
	"menuitem":         true,
	"menuitemcheckbox": true,
	"menuitemradio":    true,
	"tab":              true,
	"switch":           true,
	"slider":           true,
	"spinbutton":       true,
	"treeitem":         true,
}

// skipRoles 这些角色本身没有信息量，只作为结构容器穿透。
// 键一律小写 —— role 在渲染前已经 ToLower 过。
var skipRoles = map[string]bool{
	"none":          true,
	"generic":       true,
	"inlinetextbox": true,
	"linebreak":     true,
	"presentation":  true,
}

// emptyNameRoles 无名时不必单独占一行的结构角色（有名字时照常输出）。
var emptyNameRoles = map[string]bool{
	"paragraph":   true,
	"group":       true,
	"section":     true,
	"rootwebarea": true,
	"webarea":     true,
}

// renderAX 把无障碍树渲染成「一行一个节点」的紧凑文本。
//
// 返回：文本、ref → backendNodeId 映射、已输出节点数、是否被预算截断。
//
// 为什么用无障碍树而不是 DOM：DOM 里一半节点是布局/框架产物（div 套 div），
// 给模型看等于让它自己从噪声里找可操作元素；无障碍树天然只保留
// 「按钮 / 输入框 / 链接 / 文本」这些语义节点，且自带可访问名称。
func renderAX(nodes []*accessibility.Node, maxNodes, maxBytes int) (string, map[string]cdp.BackendNodeID, int, bool) {
	byID := make(map[accessibility.NodeID]*accessibility.Node, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}

	refs := make(map[string]cdp.BackendNodeID)
	var (
		buf     strings.Builder
		emitted int
		trunc   bool
		seq     int
	)

	var walk func(n *accessibility.Node, depth int, parentName string)
	walk = func(n *accessibility.Node, depth int, parentName string) {
		if n == nil || trunc {
			return
		}
		role := strings.ToLower(axString(n.Role))
		name := strings.TrimSpace(axString(n.Name))
		value := strings.TrimSpace(axString(n.Value))

		// 深层节点不再展开：再往下多半是表格单元格 / 列表项细节，
		// 继续渲染只会把预算花在模型根本不会点的东西上。
		if depth > 12 {
			return
		}

		emit := !n.Ignored && !skipRoles[role] && role != ""
		// 无名文本 / 无名结构容器（paragraph、group 之类）只是树的分叉点，
		// 写出来只会把快照撑成一片方括号。
		if name == "" && emptyNameRoles[role] {
			emit = false
		}
		// 文本节点与父节点同名时是同一个内容的重复投影（heading → statictext），
		// 只保留父节点那一行。
		if role == "statictext" && name == parentName {
			emit = false
		}

		childDepth := depth
		if emit {
			if emitted >= maxNodes {
				trunc = true
				return
			}
			line := strings.Repeat("  ", depth) + "- "
			if interactiveRoles[role] && n.BackendDOMNodeID != 0 {
				seq++
				ref := fmt.Sprintf("e%d", seq)
				refs[ref] = n.BackendDOMNodeID
				line += ref + " "
			}
			line += "[" + role + "]"
			if name != "" {
				line += " " + strconv.Quote(name)
			}
			if value != "" && value != name {
				line += " value=" + strconv.Quote(value)
			}
			line += renderStates(n)
			line += "\n"

			if buf.Len()+len(line) > maxBytes {
				trunc = true
				return
			}
			buf.WriteString(line)
			emitted++
			childDepth = depth + 1
		}

		nextParent := name
		if nextParent == "" {
			nextParent = parentName
		}
		for _, id := range n.ChildIDs {
			walk(byID[id], childDepth, nextParent)
			if trunc {
				return
			}
		}
	}

	// 根：父节点不在本次结果里的那些（通常是 Document，iframe 文档也会各有一个根）。
	for _, n := range nodes {
		if n.ParentID != "" {
			if _, ok := byID[n.ParentID]; ok {
				continue
			}
		}
		walk(n, 0, "")
	}

	text := buf.String()
	if trunc {
		text += "\n... (snapshot truncated; narrow it with max_nodes / max_bytes, or use text)\n"
	}
	return text, refs, emitted, trunc
}

// renderStates 把布尔/三态属性拼成 (checked, disabled) 这样的尾巴。
// 只输出 true 的状态与三态的 mixed —— 未勾选写出来只是噪声。
func renderStates(n *accessibility.Node) string {
	var out []string
	for _, p := range n.Properties {
		if p == nil || p.Value == nil {
			continue
		}
		name := string(p.Name)
		switch name {
		case "checked", "selected", "expanded", "disabled", "required", "readonly", "invalid":
			v := axString(p.Value)
			switch v {
			case "true":
				out = append(out, name)
			case "mixed":
				out = append(out, name+"=mixed")
			}
		}
	}
	if len(out) == 0 {
		return ""
	}
	return " (" + strings.Join(out, ", ") + ")"
}

// axString 取 AXValue 的字符串形态。底层是原始 JSON（jsontext.Value），
// 类型可能是字符串、数字、布尔或三态的 "mixed"/"false"。
func axString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(v.Value, &n); err == nil {
		return n.String()
	}
	var b bool
	if err := json.Unmarshal(v.Value, &b); err == nil {
		return strconv.FormatBool(b)
	}
	return ""
}
