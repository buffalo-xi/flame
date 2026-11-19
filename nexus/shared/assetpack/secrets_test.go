package assetpack

import (
	"strings"
	"testing"
)

// A global variable reference is not a credential: it is a name, and the real value lives in the
// target tenant's own {schema}.global_vars. Clearing it leaves a broken empty config after install —
// a silent breakage is harder to trace than a leak, so this has to stay locked down.
func TestStripSecretsKeepsGlobalVarRefs(t *testing.T) {
	payload := map[string]any{
		"env_vars": map[string]any{
			// References a global variable: value is a variable name and must not be cleared
			"API_TOKEN": map[string]any{
				"type":        "global",
				"value":       "CRM_API_TOKEN",
				"description": "CRM API key",
			},
			// An inline secret: still cleared (regression guard)
			"LEGACY_TOKEN": map[string]any{
				"type":  "secret",
				"value": "sk-real-secret",
			},
			// Plain text: untouched
			"REGION": map[string]any{
				"type":  "string",
				"value": "cn-north",
			},
		},
		"diagram": map[string]any{
			"nodes": []any{
				map[string]any{
					"id": "http1",
					"config": map[string]any{
						// A whole reference -> kept
						"api_key": "{{.global_vars.CRM_TOKEN}}",
						// Mixed -> kept, but warned about
						"authorization": "Bearer {{.global_vars.CRM_TOKEN}}",
						// An inline credential -> cleared
						"webhook_token": "wh-real-secret",
					},
				},
			},
		},
	}

	out, warnings := StripSecrets(KindWorkflow, payload)

	envVars := out["env_vars"].(map[string]any)
	ref := envVars["API_TOKEN"].(map[string]any)
	if ref["value"] != "CRM_API_TOKEN" {
		t.Fatalf("the global variable reference was cleared: %v", ref["value"])
	}
	if ref["type"] != "global" {
		t.Fatalf("type was modified: %v", ref["type"])
	}

	legacy := envVars["LEGACY_TOKEN"].(map[string]any)
	if legacy["value"] != "" {
		t.Fatalf("the inline secret should have been cleared, got %v", legacy["value"])
	}

	region := envVars["REGION"].(map[string]any)
	if region["value"] != "cn-north" {
		t.Fatalf("plain text was cleared by mistake: %v", region["value"])
	}

	cfg := out["diagram"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["config"].(map[string]any)
	if cfg["api_key"] != "{{.global_vars.CRM_TOKEN}}" {
		t.Fatalf("the global reference in api_key was cleared: %v", cfg["api_key"])
	}
	if cfg["authorization"] != "Bearer {{.global_vars.CRM_TOKEN}}" {
		t.Fatalf("the mixed reference was cleared: %v", cfg["authorization"])
	}
	if cfg["webhook_token"] != "" {
		t.Fatalf("the inline webhook_token should have been cleared, got %v", cfg["webhook_token"])
	}

	// The mixed case has to leave a warning asking for human confirmation; it must not be kept
	// silently
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "authorization") && strings.Contains(w, "global variable") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the mixed reference has no warning, warnings=%v", warnings)
	}
}

// The payload passed in must not be modified: the caller still uses the original value for other
// decisions.
func TestStripSecretsDoesNotMutateInput(t *testing.T) {
	payload := map[string]any{
		"diagram": map[string]any{
			"api_key": "{{.global_vars.A}}",
			"token":   "sk-real",
		},
	}
	_, _ = StripSecrets(KindWorkflow, payload)

	diagram := payload["diagram"].(map[string]any)
	if diagram["token"] != "sk-real" {
		t.Fatalf("the input was modified: %v", diagram["token"])
	}
	if diagram["api_key"] != "{{.global_vars.A}}" {
		t.Fatalf("the input was modified: %v", diagram["api_key"])
	}
}

// Once a global variable becomes the source of a node input, a credential field holds
// {{ .input.token }}: the value comes from a runtime input source and is not in the package, so
// redaction must keep it too — otherwise the install ends up with an empty config.
func TestStripSecretsKeepsInputRefs(t *testing.T) {
	payload := map[string]any{
		"diagram": map[string]any{
			"nodes": []any{
				map[string]any{
					"id": "http1",
					"config": map[string]any{
						"inputs": map[string]any{
							"token": map[string]any{"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"},
						},
						// A whole input reference -> kept
						"api_key": "{{ .input.token }}",
						// Mixed -> kept, but warned about
						"authorization": "Bearer {{ .input.token }}",
						// An inline credential -> cleared as usual
						"webhook_token": "wh-real-secret",
					},
				},
			},
		},
	}

	out, warnings := StripSecrets(KindWorkflow, payload)
	cfg := out["diagram"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["config"].(map[string]any)
	if cfg["api_key"] != "{{ .input.token }}" {
		t.Fatalf("the input reference was cleared: %v", cfg["api_key"])
	}
	if cfg["authorization"] != "Bearer {{ .input.token }}" {
		t.Fatalf("the mixed reference was cleared: %v", cfg["authorization"])
	}
	if cfg["webhook_token"] != "" {
		t.Fatalf("the inline credential should have been cleared, got %v", cfg["webhook_token"])
	}

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "authorization") && strings.Contains(w, "reference") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the mixed reference has no warning, warnings=%v", warnings)
	}
}
