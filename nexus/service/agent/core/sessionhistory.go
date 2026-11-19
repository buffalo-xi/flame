package core

import (
	"encoding/json"

	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/models"
)

// RecordUserContent extracts the user input body from one session record (input jsonb stores {"content":"..."}).
//
// A parse failure or a missing field falls back to "{}" (the behaviour before the rework): a replay would rather show a placeholder than
// fail to read a whole context because of one dirty record.
func RecordUserContent(rec models.SessionRecord) string {
	if len(rec.Input) == 0 {
		return "{}"
	}
	var in map[string]any
	if err := json.Unmarshal(rec.Input, &in); err != nil {
		return "{}"
	}
	if c, ok := in["content"].(string); ok {
		return c
	}
	return "{}"
}

// RecordsToMessages turns session records into LLM messages (user / assistant alternating).
//
// One record = one run (input + final result), not one chat message, so it is split into two here.
// It lives in core rather than at the entry layer because the web chat and the protocol endpoints must use the same conversion -
// otherwise the history read from the two entry points of one session looks different.
func RecordsToMessages(records []models.SessionRecord) []gateway.Message {
	msgs := make([]gateway.Message, 0, len(records)*2)
	for _, rec := range records {
		msgs = append(msgs, gateway.Message{Role: "user", Content: RecordUserContent(rec)})
		if rec.Output != "" {
			msgs = append(msgs, gateway.Message{Role: "assistant", Content: rec.Output})
		}
	}
	return msgs
}
