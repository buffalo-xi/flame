package assetpack

import (
	"encoding/json"
	"strings"
	"testing"
)

// The production shape: the jsonb columns in the payload are raw bytes from the database
// (json.RawMessage) rather than map[string]any — that is exactly what listing and export work with
// (see admin/share/planWorkflow). Redaction has to work on that input too, otherwise "the package
// only holds reference names" is a claim that lives in a comment.
func TestStripSecretsDecodesRawJSONB(t *testing.T) {
	payload := map[string]any{
		"name": "demo",
		"env_vars": json.RawMessage(`{
			"API_TOKEN": {"type": "secret", "value": "sk-live-leak", "description": "token label"},
			"REGION":    {"type": "string", "value": "cn"},
			"TENANT":    {"type": "global", "value": "CRM_TOKEN"}
		}`),
		"diagram": json.RawMessage(`{"nodes":[{"config":{"api_key":"sk-node-leak","host":"a.b"}}]}`),
	}

	out, warnings := StripSecrets(KindWorkflow, payload)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("serialisation failed: %v", err)
	}
	body := string(raw)

	if strings.Contains(body, "sk-live-leak") {
		t.Fatalf("the plaintext env_vars secret reached the export: %s", body)
	}
	if strings.Contains(body, "sk-node-leak") {
		t.Fatalf("the plaintext credential in diagram reached the export: %s", body)
	}
	if len(warnings) == 0 {
		t.Fatal("values were cleared without a warning: the review page would think nothing happened")
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
	envVars, _ := got["env_vars"].(map[string]any)
	if token, _ := envVars["API_TOKEN"].(map[string]any); token != nil {
		if v, _ := token["value"].(string); v != "" {
			t.Fatalf("the secret value was not cleared: %q", v)
		}
		if d, _ := token["description"].(string); d != "token label" {
			t.Fatalf("description should be kept, got %q", d)
		}
	} else {
		t.Fatalf("the env_vars structure was lost: %v", envVars)
	}
	// A global value is a variable name; clearing it breaks the reference
	if ref, _ := envVars["TENANT"].(map[string]any); ref != nil {
		if v, _ := ref["value"].(string); v != "CRM_TOKEN" {
			t.Fatalf("the global reference was cleared: %q", v)
		}
	}
}

// The global variable dependencies of the same payload must also be scannable from the raw bytes
// (otherwise an import never reports a missing variable). The production shape is raw jsonb bytes:
// without normalisation the scan silently returns an empty list.
func TestCollectGlobalVarRefsDecodesRawJSONB(t *testing.T) {
	payload := map[string]any{
		"diagram": json.RawMessage(
			`{"nodes":[{"config":{"inputs":{"base":{"type":"string","source_type":"global","source_key":"API_BASE"}}}}]}`),
	}

	refs := CollectGlobalVarRefs(nil, nil, "", payload)
	if len(refs) != 1 || refs[0] != "API_BASE" {
		t.Fatalf("refs = %v, want [API_BASE]", refs)
	}
}
