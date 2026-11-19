package tools

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/models"
)

// fakeCustomToolAdmin records calls and returns preset results on demand; it touches neither the database nor the disk.
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

// All four meta tools must sit in CategoryCustomTool: the category is the only supervisor-only criterion, and leaving it off opens
// them to every agent (the permission boundary of generative calls is decided by the category).
func TestCustomToolMetaToolsAreSupervisorOnly(t *testing.T) {
	for _, name := range []string{
		"create_custom_tool", "update_custom_tool", "delete_custom_tool", "list_custom_tools",
	} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if got := tool.Meta().Category; got != CategoryCustomTool {
			t.Errorf("%s category = %q, want %q", name, got, CategoryCustomTool)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%s should be classified as super-agent only", name)
		}
	}
}

// Name and entry validation is the only input boundary these tools have: whatever the model supplies cannot escape the directory.
func TestCreateCustomToolValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		caps    map[string]bool
		wantErr string
	}{
		// ValidName itself forbids underscores, so the "__" and prefix cases are defensive branches
		// (they only start blocking anyone if the name rule is relaxed later).
		{"underscore", map[string]any{"name": "sum_sales", "description": "d"}, nil, "letters, digits and hyphens"},
		{"with prefix", map[string]any{"name": "custom__sum", "description": "d"}, nil, "letters, digits and hyphens"},
		{"empty name", map[string]any{"name": "", "description": "d"}, nil, "must not be empty"},
		{"entry escapes the directory", map[string]any{"name": "sum", "description": "d", "entry": "../evil.py"}, nil, "must not escape"},
		{"entry is absolute", map[string]any{"name": "sum", "description": "d", "entry": "/etc/passwd"}, nil, "must be a path relative"},
		{"entry has a backslash", map[string]any{"name": "sum", "description": "d", "entry": "sub\\main.py"}, nil, "/"},
		{"exec not granted", map[string]any{"name": "sum", "description": "d"}, map[string]bool{}, capability.Exec},
		{"egress not granted", map[string]any{"name": "sum", "description": "d", "egress": true},
			map[string]bool{capability.Exec: true}, capability.Egress},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Context{AgentName: "main", CustomToolsAdmin: &fakeCustomToolAdmin{}, Caps: tc.caps}
			_, err := (&createCustomToolTool{}).Execute(context.Background(), c, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error matching %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// After a successful creation the "next steps" must be spelled out: the model should not have to guess where the code goes or how
// the grant is attached.
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
		t.Fatalf("creation should succeed: %v", err)
	}
	if admin.created == nil || admin.created.Name != "sum" {
		t.Fatalf("the input was not handed to admin: %+v", admin.created)
	}
	next, _ := out.(map[string]any)["next"].([]string)
	joined := strings.Join(next, " ")
	// The leading "/" is essential: without it the path is treated as workspace-relative, the code lands in the session workspace
	// while the tool still reads the skeleton from creation time - a model copying next verbatim falls into exactly that trap, so it
	// is pinned in a test.
	if !strings.Contains(joined, "/tools/sum/main.lua") {
		t.Errorf("next should point at the entry file under the tenant root (/tools/...), got %v", next)
	}
	if !strings.Contains(joined, "custom:sum") {
		t.Errorf("next should mention the grant ref, got %v", next)
	}
}

// Being referenced must refuse the delete and tell the model who references it - deleting silently would make someone else's tool
// directory lose an entry out of nowhere.
func TestDeleteCustomToolRequiresForce(t *testing.T) {
	admin := &fakeCustomToolAdmin{refs: &CustomToolRefs{Agents: []string{"poet"}, Groups: []string{"rpt"}}}
	c := &Context{AgentName: "main", CustomToolsAdmin: admin}

	_, err := (&deleteCustomToolTool{}).Execute(context.Background(), c, map[string]any{"name": "sum"})
	if err == nil || !strings.Contains(err.Error(), "force=true") {
		t.Fatalf("when referenced it should mention force, got %v", err)
	}
	if !strings.Contains(err.Error(), "poet") {
		t.Errorf("it should list the referencing parties, got %v", err)
	}

	admin.refs = nil
	out, err := (&deleteCustomToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "sum", "force": true,
	})
	if err != nil {
		t.Fatalf("with no references the delete should succeed: %v", err)
	}
	if !admin.deleted || !admin.force {
		t.Fatalf("force was not forwarded to admin: deleted=%v force=%v", admin.deleted, admin.force)
	}
	if deleted, _ := out.(map[string]any)["deleted"].(bool); !deleted {
		t.Errorf("the result should report the deletion: %+v", out)
	}
}

// force=true while still referenced: the delete must succeed (the reference rows are kept) and must not be mistaken for a refusal.
// This pins a real branch: the service layer returns the referencing parties whether or not force is set, so deciding by refs>0
// alone would be wrong.
func TestDeleteCustomToolForceWithRefs(t *testing.T) {
	admin := &fakeCustomToolAdmin{refs: &CustomToolRefs{Agents: []string{"poet"}}}
	c := &Context{AgentName: "main", CustomToolsAdmin: admin}

	out, err := (&deleteCustomToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "sum", "force": true,
	})
	if err != nil {
		t.Fatalf("a forced delete should succeed: %v", err)
	}
	m, _ := out.(map[string]any)
	if deleted, _ := m["deleted"].(bool); !deleted {
		t.Errorf("it should report the deletion: %+v", out)
	}
	if m["kept_refs"] == nil {
		t.Error("it should report the reference rows that were kept (recreating a tool with the same name restores the grants)")
	}
}

// When update passes only some fields, the rest must stay nil (the service layer reads nil as "leave unchanged").
func TestUpdateCustomToolPartial(t *testing.T) {
	admin := &fakeCustomToolAdmin{}
	c := &Context{AgentName: "main", CustomToolsAdmin: admin}
	_, err := (&updateCustomToolTool{}).Execute(context.Background(), c, map[string]any{
		"name": "sum", "description": "新描述",
	})
	if err != nil {
		t.Fatalf("the update should succeed: %v", err)
	}
	if admin.updated == nil || admin.updated.Description == nil {
		t.Fatalf("description was not forwarded: %+v", admin.updated)
	}
	if admin.updated.Lang != nil || admin.updated.Entry != nil || admin.updated.Egress != nil {
		t.Errorf("fields that were not passed should stay nil: %+v", admin.updated)
	}
}

// Without admin injected (a caller with no tenant semantics) give a readable error rather than a nil dereference.
func TestCustomToolMetaWithoutAdmin(t *testing.T) {
	c := &Context{AgentName: "main"}
	for _, tool := range []Tool{
		&createCustomToolTool{}, &updateCustomToolTool{},
		&deleteCustomToolTool{}, &listCustomToolsTool{},
	} {
		args := map[string]any{"name": "sum", "description": "d"}
		if _, err := tool.Execute(context.Background(), c, args); err == nil ||
			!strings.Contains(err.Error(), "is not available") {
			t.Errorf("%s should report that admin is unavailable, got %v", tool.Meta().Name, err)
		}
	}
}
