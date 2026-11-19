package assetpack

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// secretKeyRe marks a key whose value may be a credential.
//
// Deliberately over-eager: clearing a normal field costs the user one refill, while missing one
// api_key is a cross-tenant leak.
var secretKeyRe = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|passwd|credential|authorization)`)

// A deferred reference is not a credential: it is only a name or a placeholder, and the real value
// either lives in the target tenant's {schema}.global_vars or is supplied by a node input variable
// at runtime. Clearing it to the empty string would break the reference after install — a silent
// breakage that is harder to trace than a leak.
//
// The three forms are defined in globalrefs.go (shared with the requirement-list scan):
// {{ .input.NAME }} (current), {{.global_vars.NAME}} and ${global_vars.NAME} (docs / legacy).

// isDeferredRef reports whether a string value is exactly one reference (the value is not in the
// package).
func isDeferredRef(s string) bool {
	return s != "" && globalRefExactRe.MatchString(s)
}

// containsDeferredRef reports whether a value mixes in a reference (e.g.
// "Bearer {{ .input.TOKEN }}").
func containsDeferredRef(s string) bool {
	return s != "" && (refInputRe.MatchString(s) || refTemplateRe.MatchString(s) || refDocRe.MatchString(s))
}

// StripSecrets removes sensitive values from the snapshot payload and returns the cleaned copy plus
// warnings.
//
// Rules:
//   - custom_tool: `configs` is always cleared to an empty object. Runtime config values are almost
//     all credentials (tokens, webhooks, database passwords) and carrying them across tenants is a
//     leak; `config_fields` (the declared config items) is kept, and the consumer fills it in after
//     install.
//   - agent: inside `config`, any string value at any depth whose key matches a credential name is
//     emptied (model api_key, RPC connection passwords may all sit here), with one warning per
//     removal so the review page can be compared line by line.
//   - workflow: both `env_vars` and `diagram` (which holds the model api_key / webhook token of
//     node configs) are scanned with the same key rules, again warning per removal.
//   - skill: has no credential fields, returned as is.
//
// The payload passed in is not modified (it is deep-copied first), so the caller can keep using it
// for other checks.
func StripSecrets(kind Kind, payload map[string]any) (map[string]any, []string) {
	out := deepCopyMap(payload)
	if out == nil {
		out = map[string]any{}
	}
	var warnings []string

	switch kind {
	case KindTool:
		if v, ok := payload["configs"]; ok {
			had := 0
			if m, isMap := v.(map[string]any); isMap {
				had = len(m)
			}
			out["configs"] = map[string]any{}
			if had > 0 {
				warnings = append(warnings, warn(
					"cleared %d config value(s): the config_fields declarations stay, but they have to be filled in again in the target tenant after install", had))
			}
		}
	case KindAgent:
		if cfg, ok := out["config"].(map[string]any); ok {
			stripSecretKeys(cfg, "config", &warnings)
		}
	case KindWorkflow:
		// The two layers are handled separately because their shapes differ:
		//
		//   env_vars is the structured table {name: {type, value, description}} —
		//     the decision is on type, not on the key name. Keys are variable names (API_KEY /
		//     DB_PASSWORD match the credential regex by name, so clearing by name would hit the
		//     wrong thing), and the value always sits in value, so scanning by name would never
		//     reach it — which is exactly why "inline secrets in env_vars were not actually
		//     cleared" used to happen. It is decided by type now:
		//       secret → clear value (an inline secret never travels across tenants)
		//       global → deprecated, keep value when it is a variable name and suggest migrating
		//                (clearing it leaves no trace of what it pointed at)
		//       other  → untouched
		//
		//   diagram is a free-form structure (node configs) and can only be scanned by key name.
		if m, ok := out["env_vars"].(map[string]any); ok {
			stripEnvVars(m, &warnings)
		}
		if m, ok := out["diagram"].(map[string]any); ok {
			stripSecretKeys(m, "diagram", &warnings)
		}
	}

	return out, warnings
}

// PreserveSecretKeys restores, on an overwriting install, the credential keys the package cleared
// to the target's existing values (it modifies incoming in place).
//
// Listing clears credential values to the empty string, so an overwriting install that replaced the
// whole object would wipe the api_key / token the target tenant already configured — an overwrite
// replaces content, not credentials. This is the same convention as placeTool keeping
// prev.Configs and a workflow keeping the secret env_vars by variable name, except that an agent's
// config is free-form with no type to rely on, so it falls back to key names.
//
// Only "the package has the empty string and the key looks like a credential" is handled: a
// non-empty value in the package wins, and empty fields whose key does not look like a credential
// are left alone (they may legitimately be empty). The recursion depth matches StripSecrets.
//
// The cost matches a workflow's secret: deleting one credential by clearing it is impossible, only
// deleting the whole item and recreating it works. Returns the number of restored keys so the caller
// can mention it in the install result.
func PreserveSecretKeys(incoming, target map[string]any) int {
	if len(incoming) == 0 || len(target) == 0 {
		return 0
	}
	kept := 0
	for key, val := range incoming {
		switch v := val.(type) {
		case map[string]any:
			if t, ok := target[key].(map[string]any); ok {
				kept += PreserveSecretKeys(v, t)
			}
		case string:
			if v != "" || !secretKeyRe.MatchString(key) {
				continue
			}
			if t, ok := target[key].(string); ok && t != "" {
				incoming[key] = t
				kept++
			}
		}
	}
	return kept
}

// stripEnvVars handles the environment variable table by type (see the KindWorkflow notes in
// StripSecrets).
//
// Two forms are supported: the structured {type, value, description} and the early flat
// {"NAME": "value"}.
func stripEnvVars(envVars map[string]any, warnings *[]string) {
	for name, raw := range envVars {
		item, ok := raw.(map[string]any)
		if !ok {
			// Flat form: only the key name is left to go on, and global variable references must
			// be excluded
			s, isStr := raw.(string)
			if !isStr || s == "" || isDeferredRef(s) || !secretKeyRe.MatchString(name) {
				continue
			}
			envVars[name] = ""
			*warnings = append(*warnings, warn("env_vars.%s looks like an inline credential and was cleared", name))
			continue
		}

		switch kind, _ := item["type"].(string); kind {
		case "secret":
			if s, _ := item["value"].(string); s != "" {
				item["value"] = ""
				*warnings = append(*warnings,
					warn("env_vars.%s is an inline secret and was cleared (fill it in again after install)", name))
			}
		case "global":
			// A deprecated type (global variables became a source of node inputs): value is still a
			// variable name rather than a value, so it is not cleared — clearing it would leave an
			// old package with no trace of what it pointed at.
			*warnings = append(*warnings,
				warn("env_vars.%s uses the deprecated type global; after import switch it to a global-variable input source", name))
		}
	}
}

// stripSecretKeys recursively empties string values whose key looks like a credential.
//
// It walks maps and []any level by level (that is what jsonb decodes into); prefix is only there to
// locate the value in a warning.
func stripSecretKeys(m map[string]any, prefix string, warnings *[]string) {
	for k, v := range m {
		key := prefix + "." + k
		switch val := v.(type) {
		case map[string]any:
			// The deprecated environment variable declaration {type: "global", value: "NAME"}:
			// value is a variable name, not a value, so the whole item is skipped (stripEnvVars
			// records a separate migration hint).
			if kind, _ := val["type"].(string); kind == "global" {
				continue
			}
			stripSecretKeys(val, key, warnings)
		case []any:
			for i, item := range val {
				if sub, ok := item.(map[string]any); ok {
					stripSecretKeys(sub, key+"["+strconv.Itoa(i)+"]", warnings)
				}
			}
		default:
			if !secretKeyRe.MatchString(k) {
				continue
			}
			// The key looks like a credential but the value is a reference: keep it.
			// For example {"api_key": "{{ .input.TOKEN }}"}: the value comes from an input
			// variable source and is not in the package; clearing it would turn the reference into
			// an empty string and leave the target tenant with a broken empty config.
			if s, ok := v.(string); ok {
				if isDeferredRef(s) {
					continue
				}
				// Mixed case ("Bearer {{ .input.TOKEN }}"): clearing it breaks things outright, so
				// keep it and warn, letting the reviewer decide whether an inline credential hides
				// in there.
				if containsDeferredRef(s) {
					*warnings = append(*warnings,
						warn("%s contains references (input / global variables) and was kept (make sure it holds no inline credential)", key))
					continue
				}
				if s == "" {
					continue // already empty, do not add noise
				}
			}
			m[k] = ""
			*warnings = append(*warnings, warn("%s looks like a credential and was cleared", key))
		}
	}
}

// deepCopyMap deep-copies a JSON-shaped map (only map[string]any / []any / scalars).
//
// jsonb decodes into exactly these shapes; there is no generic reflection copy so that values such
// as time.Time are not mangled.
func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return deepCopyMap(val)
	case []any:
		cp := make([]any, len(val))
		for i := range val {
			cp[i] = deepCopyValue(val[i])
		}
		return cp
	case json.RawMessage:
		// The production shape: jsonb columns inside the payload arrive as raw bytes from the
		// database. Without decoding, every map-based decision below (redaction, dependency scan)
		// silently skips — the comment says "cleared" while the package carries plaintext.
		return decodeJSONValue(val)
	case []byte:
		return decodeJSONValue(val)
	default:
		return v
	}
}

// decodeJSONValue turns raw jsonb bytes into a map / []any / scalar.
//
// When it cannot decode it returns an empty object rather than the original bytes: the only purpose
// on this path is redaction, and walking on with bytes we do not understand most likely ends with
// them being written into the download package verbatim.
func decodeJSONValue(b []byte) any {
	if len(b) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return map[string]any{}
	}
	return v
}
