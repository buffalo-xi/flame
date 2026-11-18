package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/models"
)

// fakeCustomToolAdmin 记录调用、按需返回预置结果；不碰库、不碰盘。
type fakeCustomToolAdmin struct {
	created *CustomToolInput
	updated *CustomToolUpdate
	deleted bool
	force   bool
	refs    *CustomToolRefs
	list    []CustomToolSpec
}

func (f *fakeCustomToolAdmin) Create(_ context.Context, in CustomToolInput) (*CustomToolSpec, error) {
	f.created = &in
	return &CustomToolSpec{Name: in.Name, Status: models.StatusNormal}, nil
}

func (f *fakeCustomToolAdmin) Update(_ context.Context, name string, in CustomToolUpdate) (*CustomToolSpec, error) {
	f.updated = &in
	return &CustomToolSpec{Name: name, Status: models.StatusNormal}, nil
}

func (f *fakeCustomToolAdmin) Delete(_ context.Context, name string, force bool) (*CustomToolRefs, error) {
	f.deleted = true
	f.force = force
	return f.refs, nil
}

func (f *fakeCustomToolAdmin) ListAll(context.Context) ([]CustomToolSpec, error) { return f.list, nil }

// 四个元工具都必须落在 CategoryCustomTool：supervisor-only 的唯一判据是分类，
// 漏标就等于把它们开放给所有 agent（生成式调用的权限边界由分类决定）。
func TestCustomToolMetaToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{
		"create_custom_tool", "update_custom_tool", "delete_custom_tool", "list_custom_tools",
	} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("%s 未注册", name)
		}
		if got := tool.Meta().Category; got != CategoryCustomTool {
			t.Errorf("%s 分类 = %q, want %q", name, got, CategoryCustomTool)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%s 应判定为仅超级智能体可用", name)
		}
	}
}

// 名字与入口的校验是这批工具唯一的输入边界：模型给什么都不能越过目录。
func TestCreateCustomToolValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		caps    map[string]bool
		wantErr string
	}{
		// ValidName 本身就不允许下划线，所以 "__" / 前缀这两条属于防御性分支
		// （将来若放宽名字规则，它们才会真正拦人）。
		{"下划线", map[string]any{"name": "sum_sales", "description": "d"}, nil, "字母、数字与短横线"},
		{"带前缀", map[string]any{"name": "custom__sum", "description": "d"}, nil, "字母、数字与短横线"},
		{"空名", map[string]any{"name": "", "description": "d"}, nil, "不能为空"},
		{"entry 跳出目录", map[string]any{"name": "sum", "description": "d", "entry": "../evil.py"}, nil, "跳出"},
		{"entry 绝对路径", map[string]any{"name": "sum", "description": "d", "entry": "/etc/passwd"}, nil, "相对"},
		{"entry 反斜杠", map[string]any{"name": "sum", "description": "d", "entry": "sub\\main.py"}, nil, "/"},
		{"未开通 exec", map[string]any{"name": "sum", "description": "d"}, map[string]bool{}, capability.Exec},
		{"egress 未开通", map[string]any{"name": "sum", "description": "d", "egress": true},
			map[string]bool{capability.Exec: true}, capability.Egress},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Context{AgentName: "main", CustomToolsAdmin: &fakeCustomToolAdmin{}, Caps: tc.caps}
			_, err := (&createCustomToolTool{}).Execute(context.Background(), c, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("应报 %q，实际 %v", tc.wantErr, err)
			}
		})
	}
}

// 创建成功后要把「下一步」写清楚：模型不该猜代码放哪、授权怎么挂。
func TestCreateCustomToolHints(t *testing.T) {
	admin := &fakeCustomToolAdmin{}
	c := &Context{
		AgentName:        "main",
		CustomToolsAdmin: admin,
		Caps:             map[string]bool{capability.Exec: true},
	}
	out, err := (&createCustomToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "sum", "description": "求和", "lang": "lua",
	})
	if err != nil {
		t.Fatalf("创建应成功: %v", err)
	}
	if admin.created == nil || admin.created.Name != "sum" {
		t.Fatalf("未把入参交给 admin: %+v", admin.created)
	}
	next, _ := out.(map[string]any)["next"].([]string)
	joined := strings.Join(next, " ")
	// 前导 "/" 不能少：不带它的路径会被当成工作区相对路径，代码写进会话工作区、
	// 工具仍读 create 时的骨架 —— 模型照抄 next 就会踩这个坑，所以钉死在测试里。
	if !strings.Contains(joined, "/tools/sum/main.lua") {
		t.Errorf("next 应指向租户根下的入口文件（/tools/...），实际 %v", next)
	}
	if !strings.Contains(joined, "custom:sum") {
		t.Errorf("next 应提示授权 ref，实际 %v", next)
	}
}

// 被引用时必须拒绝，且把引用方告诉模型 —— 静默删除等于让别人的工具目录凭空少一项。
func TestDeleteCustomToolRequiresForce(t *testing.T) {
	admin := &fakeCustomToolAdmin{refs: &CustomToolRefs{Agents: []string{"poet"}, Groups: []string{"rpt"}}}
	c := &Context{AgentName: "main", CustomToolsAdmin: admin}

	_, err := (&deleteCustomToolTool{}).Execute(context.Background(), c, map[string]any{"name": "sum"})
	if err == nil || !strings.Contains(err.Error(), "force=true") {
		t.Fatalf("被引用时应提示 force，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "poet") {
		t.Errorf("应列出引用方，实际 %v", err)
	}

	admin.refs = nil
	out, err := (&deleteCustomToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "sum", "force": true,
	})
	if err != nil {
		t.Fatalf("无人引用时应删除成功: %v", err)
	}
	if !admin.deleted || !admin.force {
		t.Fatalf("force 未透传给 admin: deleted=%v force=%v", admin.deleted, admin.force)
	}
	if deleted, _ := out.(map[string]any)["deleted"].(bool); !deleted {
		t.Errorf("返回值应报告已删除: %+v", out)
	}
}

// force=true 且仍被引用：删除必须成功（引用行保留），不能被误判成「拒绝」。
// 这条钉住的是一个真实分支：服务层无论 force 与否都会返回引用方，只靠 refs>0 判拒绝会错。
func TestDeleteCustomToolForceWithRefs(t *testing.T) {
	admin := &fakeCustomToolAdmin{refs: &CustomToolRefs{Agents: []string{"poet"}}}
	c := &Context{AgentName: "main", CustomToolsAdmin: admin}

	out, err := (&deleteCustomToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "sum", "force": true,
	})
	if err != nil {
		t.Fatalf("force 删除应成功: %v", err)
	}
	m, _ := out.(map[string]any)
	if deleted, _ := m["deleted"].(bool); !deleted {
		t.Errorf("应报告已删除: %+v", out)
	}
	if m["kept_refs"] == nil {
		t.Error("应报告被保留的引用行（重建同名工具可恢复授权）")
	}
}

// update 只传部分字段时，其余必须保持 nil（服务层据此判断「不改」）。
func TestUpdateCustomToolPartial(t *testing.T) {
	admin := &fakeCustomToolAdmin{}
	c := &Context{AgentName: "main", CustomToolsAdmin: admin}
	_, err := (&updateCustomToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "sum", "description": "新描述",
	})
	if err != nil {
		t.Fatalf("更新应成功: %v", err)
	}
	if admin.updated == nil || admin.updated.Description == nil {
		t.Fatalf("description 未透传: %+v", admin.updated)
	}
	if admin.updated.Lang != nil || admin.updated.Entry != nil || admin.updated.Egress != nil {
		t.Errorf("未传的字段应保持 nil: %+v", admin.updated)
	}
}

// 未注入 admin（无租户语义的调用方）时给可读错误，而不是 nil 解引用。
func TestCustomToolMetaWithoutAdmin(t *testing.T) {
	c := &Context{AgentName: "main"}
	for _, tool := range []Tool{
		&createCustomToolTool{}, &updateCustomToolTool{},
		&deleteCustomToolTool{}, &listCustomToolsTool{},
	} {
		args := map[string]any{"name": "sum", "description": "d"}
		if _, err := tool.Execute(context.Background(), c, args); err == nil ||
			!strings.Contains(err.Error(), "不可用") {
			t.Errorf("%s 应报 admin 不可用，实际 %v", tool.Meta().Name, err)
		}
	}
}
