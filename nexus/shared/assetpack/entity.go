package assetpack

import "encoding/json"

// PortableSkill narrows a skills row (the result of `SELECT to_jsonb(s) FROM <schema>.skills s`)
// down to a snapshot payload that can cross tenants.
//
// It is a whitelist rather than a blacklist: a new column is **not** carried by default, since
// missing one loses a feature while carrying one extra may carry identity (id / agent_id) or audit
// fields (created_by / timestamps) — those have to be regenerated in the target tenant, and copying
// them only leaves the two sides with mismatched ids.
//
// status is deliberately not carried: whether an install is enabled or disabled is decided by the
// install request (enabled by default), and the source tenant's state must not decide for the
// target tenant.
func PortableSkill(row map[string]any) map[string]any {
	out := make(map[string]any, 8)
	for _, k := range []string{
		"name", "description", "metadata",
		"user_invocable", "command_dispatch", "command_tool",
		"restricted",
	} {
		if v, ok := row[k]; ok {
			out[k] = v
		}
	}
	return out
}

// EncodeJSONB encodes any value into bytes that can go straight into a jsonb argument (nil → "{}").
//
// Snapshot payloads and manifests both land in jsonb; everything goes through here so nil is handled
// consistently everywhere (the sql layer treats a nil []byte as SQL NULL, while the column is NOT
// NULL).
func EncodeJSONB(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}

// DecodeJSONB decodes a jsonb column into a map (NULL / empty → empty map).
func DecodeJSONB(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}
