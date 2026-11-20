package flow

import (
	"strings"
	"testing"
)

// The env var type "global" is retired (global variables became a node input source).
// Legacy data may still carry it: it must produce a readable sentence, not be silently treated as a
// plain string or yield an empty value.
func TestGetEnvVarRejectsDeprecatedGlobalType(t *testing.T) {
	envVars := map[string]interface{}{
		"API_TOKEN": map[string]interface{}{"type": "global", "value": "CRM_TOKEN"},
		"INLINE":    map[string]interface{}{"type": "string", "value": "hello"},
	}

	t.Run("the global type reports the deprecation", func(t *testing.T) {
		_, err := GetEnvVar(envVars, "API_TOKEN")
		if err == nil {
			t.Fatal("expected an error, got success")
		}
		if !strings.Contains(err.Error(), "deprecated") {
			t.Fatalf("the error should say the type is deprecated, got: %v", err)
		}
	})

	t.Run("inline values are unaffected", func(t *testing.T) {
		v, err := GetEnvVar(envVars, "INLINE")
		if err != nil {
			t.Fatalf("an inline value must not fail: %v", err)
		}
		if v != "hello" {
			t.Fatalf("wrong value: %v", v)
		}
	})

	t.Run("a missing path is an error", func(t *testing.T) {
		if _, err := GetEnvVar(envVars, "MISSING"); err == nil {
			t.Fatal("expected an error, got success")
		}
	})
}

// A global variable as an input source (source_type: "global"): both cases are decidable without
// touching the database and must be rejected up front.
func TestResolveGlobalSourceGuards(t *testing.T) {
	t.Run("an empty name is an error", func(t *testing.T) {
		_, err := ResolveGlobalSource("t0", "  ")
		if err == nil {
			t.Fatal("expected an error, got success")
		}
		if !strings.Contains(err.Error(), "no global variable name") {
			t.Fatalf("the error should point at the missing name, got: %v", err)
		}
	})

	t.Run("a missing tenant context is an error", func(t *testing.T) {
		_, err := ResolveGlobalSource("", "DB_HOST")
		if err == nil {
			t.Fatal("expected an error, got success")
		}
		if !strings.Contains(err.Error(), "no tenant schema") {
			t.Fatalf("the error should mention the missing tenant context, got: %v", err)
		}
	})
}

// The retired global type cannot be assigned at run time: the error must point at the migration, and
// the original config must stay intact.
func TestSetEnvVarRejectsDeprecatedGlobalType(t *testing.T) {
	envVars := map[string]interface{}{
		"API_TOKEN": map[string]interface{}{"type": "global", "value": "CRM_TOKEN"},
	}
	err := SetEnvVar(envVars, "API_TOKEN", "literal")
	if err == nil {
		t.Fatal("assigning to the deprecated global type must be rejected")
	}
	if !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("the error should say the type is deprecated, got: %v", err)
	}
	item := envVars["API_TOKEN"].(map[string]interface{})
	if item["value"] != "CRM_TOKEN" || item["type"] != "global" {
		t.Fatalf("the original config was modified: %v", item)
	}
}
