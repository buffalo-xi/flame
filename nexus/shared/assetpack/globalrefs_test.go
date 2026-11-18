package assetpack

import (
	"context"
	"testing"
)

// 两个来源都要扫到：
//   - 输入变量的 source_type: "global"（现行写法 —— 里面连 global_vars 字样都没有，
//     只靠正则会漏，这正是它单独一条扫描路径的原因）；
//   - 节点模板里的 {{.global_vars.X}}（旧写法残留：虽然已经跑不起来，但删变量前仍要看见它）。
func TestCollectGlobalVarRefsFromWorkflowPayload(t *testing.T) {
	payload := map[string]any{
		"diagram": map[string]any{
			"nodes": []any{
				map[string]any{"config": map[string]any{
					"inputs": map[string]any{
						"token":  map[string]any{"type": "string", "source_type": "global", "source_key": "CRM_API_TOKEN"},
						"region": map[string]any{"type": "string", "source_type": "global", "source_key": "REGION"},
						"plain":  map[string]any{"type": "string", "source_type": "env", "source_key": "NOT_A_GLOBAL"},
					},
					"url": "https://{{.global_vars.DB_HOST}}/api",
				}},
			},
		},
	}

	got := CollectGlobalVarRefs(context.Background(), nil, "", payload)
	want := []string{"CRM_API_TOKEN", "DB_HOST", "REGION"}
	if len(got) != len(want) {
		t.Fatalf("引用清单 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("引用清单 = %v, want %v（要求去重且升序）", got, want)
		}
	}
}

// 没有定义域目录时不能报错：工作流没有 srcDir，这是正常情况。
func TestCollectGlobalVarRefsWithoutStorage(t *testing.T) {
	got := CollectGlobalVarRefs(context.Background(), nil, "", map[string]any{"name": "demo"})
	if len(got) != 0 {
		t.Fatalf("无引用应返回空，实际 %v", got)
	}
}

// 两种写法都要认；顺序不保证（两套正则分开扫），所以只断言集合。
func TestGlobalVarRefsInText(t *testing.T) {
	got := GlobalVarRefsInText("a ${global_vars.A} b {{.global_vars.B}} c")
	if len(got) != 2 {
		t.Fatalf("GlobalVarRefsInText = %v, want 2 项", got)
	}
	set := map[string]bool{got[0]: true, got[1]: true}
	if !set["A"] || !set["B"] {
		t.Fatalf("GlobalVarRefsInText = %v, want A 与 B", got)
	}
}
