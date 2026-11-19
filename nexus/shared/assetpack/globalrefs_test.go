package assetpack

import (
	"context"
	"testing"
)

// Both sources have to be found:
//   - input variables with source_type: "global" (the current form — it does not even contain the
//     text global_vars, so regexes alone would miss it, which is why it has its own scan path);
//   - {{.global_vars.X}} in node templates (leftovers of the old form: it no longer runs, but it
//     still has to be visible before a variable is deleted).
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
		t.Fatalf("reference list = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reference list = %v, want %v (deduplicated and sorted)", got, want)
		}
	}
}

// Having no definition-domain directory must not be an error: a workflow has no srcDir, which is
// normal.
func TestCollectGlobalVarRefsWithoutStorage(t *testing.T) {
	got := CollectGlobalVarRefs(context.Background(), nil, "", map[string]any{"name": "demo"})
	if len(got) != 0 {
		t.Fatalf("no references should return empty, got %v", got)
	}
}

// Both forms are recognised; the order is not guaranteed (two regexes scan separately), so only the
// set is asserted.
func TestGlobalVarRefsInText(t *testing.T) {
	got := GlobalVarRefsInText("a ${global_vars.A} b {{.global_vars.B}} c")
	if len(got) != 2 {
		t.Fatalf("GlobalVarRefsInText = %v, want 2 entries", got)
	}
	set := map[string]bool{got[0]: true, got[1]: true}
	if !set["A"] || !set["B"] {
		t.Fatalf("GlobalVarRefsInText = %v, want A and B", got)
	}
}
