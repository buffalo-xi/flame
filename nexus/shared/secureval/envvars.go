package secureval

import (
	"encoding/json"
	"fmt"
)

// Credential handling for the structured environment variable table (flows.env_vars).
//
// Shape: {variable: {type, value, description}}, where the value of type == "secret" is a credential. The
// decision is made on type rather than on the key name: keys are whatever the user invented (an API_KEY may
// well be called A), so scanning by name is guaranteed to miss some.
//
// This file wraps four things that are all views of the same JSON shape, and keeping them together is what
// stops them from drifting apart:
//   - SealEnvVars / OpenEnvVars   encryption at storage time (deterministic, see the detPrefix comment)
//   - MaskEnvVars                 outbound masking: the API returns no plaintext, only has_value
//   - MergeEnvVarSecrets          inbound merging: a secret left empty means "keep the current value"
//
// The flat legacy form {variable: "value"} has no type and cannot be classified, so it is always kept as is —
// that is how the existing data is stored, and guessing a type would rewrite the user's semantics.

const envVarTypeSecret = "secret"

// SealEnvVars encrypts the type=secret values in env_vars.
//
// Deterministic rather than random-nonce encryption: this whole JSON column takes part in the version
// comparison (flows.env_vars against flow_versions.env_vars with IS DISTINCT FROM), and a random nonce would
// make a save that only changed the description look like "the variables changed too".
func SealEnvVars(raw []byte) ([]byte, error) {
	return editSecretEnvVars(raw, func(_ string, fields map[string]json.RawMessage, value string) (bool, error) {
		dirty := false

		// has_value is an outbound field (MaskEnvVars produces it) and must not be stored: it is derived from
		// value, so storing it is just a second copy of the truth that can drift — if someone clears value
		// directly in the database, the row would hold the contradiction "empty value + has_value=true".
		// Storage is where every write path necessarily passes (both Create and Update), so removing it here is
		// the reliable spot.
		if _, ok := fields["has_value"]; ok {
			delete(fields, "has_value")
			dirty = true
		}

		if value == "" {
			return dirty, nil
		}
		sealed, err := EncryptDeterministic(value)
		if err != nil {
			return false, err
		}
		if sealed == value {
			return dirty, nil // already ciphertext
		}
		fields["value"] = mustJSON(sealed)
		return true, nil
	})
}

// OpenEnvVars restores the ciphertext in env_vars back to plaintext. Anything that is not ciphertext (including
// legacy plaintext) is left alone.
//
// For server-side use only (the execution context, asset package export); anything going out to the frontend
// must go through MaskEnvVars first.
func OpenEnvVars(raw []byte) ([]byte, error) {
	return editSecretEnvVars(raw, func(_ string, fields map[string]json.RawMessage, value string) (bool, error) {
		if value == "" {
			return false, nil
		}
		opened, err := Decrypt(value)
		if err != nil {
			return false, err
		}
		if opened == value {
			return false, nil // legacy plaintext
		}
		fields["value"] = mustJSON(opened)
		return true, nil
	})
}

// MaskEnvVars is the outbound form: the type=secret value is cleared and a has_value flag tells the frontend
// whether it was ever set.
//
// Why an empty string instead of a mask such as ••••••: the frontend sends the whole env_vars back when saving,
// and a mask string would be written back as a real new value. Empty plus has_value makes "empty = keep" hold
// in the control flow instead of relying on the frontend remembering not to submit the mask.
func MaskEnvVars(raw []byte) ([]byte, error) {
	return editSecretEnvVars(raw, func(_ string, fields map[string]json.RawMessage, value string) (bool, error) {
		fields["value"] = mustJSON("")
		fields["has_value"] = mustJSON(value != "")
		return true, nil
	})
}

// MergeEnvVarSecrets is the inbound form: an empty secret value means "keep the current one", so it is filled
// back in from the old value.
//
// The backend has to carry this rule: the UI never echoes the plaintext, so a user who does not change a
// password necessarily submits empty. When there is no old value (or the old value is empty too) it stays empty
// — that is "a variable was created and not filled in yet".
//
// The cost, stated plainly: a secret value cannot be cleared by emptying it; the only way to clear it is to
// delete the whole variable and create it again.
func MergeEnvVarSecrets(oldRaw, newRaw []byte) ([]byte, error) {
	old := secretValues(oldRaw)
	if len(old) == 0 {
		return newRaw, nil
	}
	return editSecretEnvVars(newRaw, func(name string, fields map[string]json.RawMessage, value string) (bool, error) {
		if value != "" {
			return false, nil
		}
		prev, ok := old[name]
		if !ok || prev == "" {
			return false, nil
		}
		fields["value"] = mustJSON(prev)
		return true, nil
	})
}

// secretValues reads the old secret plaintext values (keyed by variable name). A parse failure always counts as
// "no old values": this is a helper read on the merge path and must not fail the whole save — the real error
// shows up before the write.
func secretValues(raw []byte) map[string]string {
	out := make(map[string]string)
	if len(raw) == 0 {
		return out
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return out
	}
	for name, item := range top {
		_, value, ok := decodeSecretEntry(item)
		if !ok || value == "" {
			continue
		}
		out[name] = value
	}
	return out
}

// editSecretEnvVars walks the type=secret entries and hands each decoded field table to fn for in-place editing.
// fn returns true to mean "this one changed", which decides whether the document needs re-serialising.
//
// Only the touched fields are rewritten; everything else (description, ...) is carried through as
// map[string]json.RawMessage — decoding into a struct would silently drop fields added in the future on the
// round trip.
func editSecretEnvVars(raw []byte, fn func(name string, fields map[string]json.RawMessage, value string) (bool, error)) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("env_vars is not a JSON object: %w", err)
	}

	changed := false
	for name, item := range top {
		fields, value, ok := decodeSecretEntry(item)
		if !ok {
			continue // the flat legacy form, or not a secret: leave it alone
		}
		dirty, err := fn(name, fields, value)
		if err != nil {
			return nil, fmt.Errorf("failed to process environment variable %s: %w", name, err)
		}
		if !dirty {
			continue
		}
		patched, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		top[name] = patched
		changed = true
	}

	if !changed {
		return raw, nil
	}
	return json.Marshal(top)
}

// decodeSecretEntry reports whether an item is a structured type=secret entry, returning its field table and
// current value.
func decodeSecretEntry(item json.RawMessage) (map[string]json.RawMessage, string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item, &fields); err != nil {
		return nil, "", false
	}
	var typ string
	if v, ok := fields["type"]; ok {
		_ = json.Unmarshal(v, &typ)
	}
	if typ != envVarTypeSecret {
		return nil, "", false
	}
	var value string
	if v, ok := fields["value"]; ok {
		if err := json.Unmarshal(v, &value); err != nil {
			return nil, "", false // value is not a string (dirty data): leave it alone
		}
	}
	return fields, value, true
}

// mustJSON encodes a string / bool as a JSON literal. Every input here is produced by this package (string or
// bool), so encoding cannot fail and the error branch is skipped.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}
