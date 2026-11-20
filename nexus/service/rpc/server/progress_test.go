package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flame/nexus/service/agent/core"
)

// parseProgressFrames extracts the params of every notifications/progress frame from an SSE body.
func parseProgressFrames(t *testing.T, body string) []rpcProgressMsg {
	t.Helper()
	var out []rpcProgressMsg
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var prog rpcProgress
		if err := json.Unmarshal([]byte(payload), &prog); err != nil {
			t.Fatalf("failed to parse the frame: %v (%s)", err, payload)
		}
		if prog.Method != "notifications/progress" {
			continue
		}
		out = append(out, prog.Params)
	}
	return out
}

// TestExtractProgressToken: the subscription intent is read only from params._meta.progressToken, and a
// value of the wrong type (neither string nor integer) always counts as "not subscribed".
func TestExtractProgressToken(t *testing.T) {
	cases := []struct {
		name   string
		params any
		want   any
	}{
		{
			name:   "string token",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": "tok-1"}},
			want:   "tok-1",
		},
		{
			name:   "integer token",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": float64(7)}},
			want:   float64(7),
		},
		{
			name:   "no _meta",
			params: map[string]any{"name": "x"},
			want:   nil,
		},
		{
			name:   "_meta is not an object",
			params: map[string]any{"name": "x", "_meta": "oops"},
			want:   nil,
		},
		{
			name:   "a non-integer token counts as not supplied",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": 1.5}},
			want:   nil,
		},
		{
			name:   "an empty string token counts as not supplied",
			params: map[string]any{"name": "x", "_meta": map[string]any{"progressToken": ""}},
			want:   nil,
		},
		{
			name:   "params is not an object",
			params: "oops",
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractProgressToken(c.params)
			if got != c.want {
				t.Fatalf("extractProgressToken() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestNotifyProgressFrames: notifications must echo the token and progress must increase strictly;
// empty content is not pushed and must not burn a sequence number (otherwise numbers would skip).
func TestNotifyProgressFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	pump := core.NewProgressPump("tok-1")

	notifyProgress(rec, pump, core.CategoryText, "a", nil)
	notifyProgress(rec, pump, core.CategoryThinking, "", nil) // empty content: not sent, no number consumed
	notifyProgress(rec, pump, core.CategoryToolCall, "b", map[string]any{"call_id": "c1"})

	frames := parseProgressFrames(t, rec.Body.String())
	if len(frames) != 2 {
		t.Fatalf("frame count = %d, want 2 (empty content must not be sent): %s", len(frames), rec.Body.String())
	}
	for i, f := range frames {
		if f.ProgressToken != "tok-1" {
			t.Errorf("frame %d token = %v, want tok-1", i, f.ProgressToken)
		}
		if want := float64(i + 1); f.Progress != want {
			t.Errorf("frame %d progress = %v, want %v (must increase monotonically)", i, f.Progress, want)
		}
	}
	if frames[0].Message != "a" || frames[1].Message != "b" {
		t.Errorf("message was not passed through correctly: %+v", frames)
	}
	if frames[0].Meta["category"] != core.CategoryText {
		t.Errorf("_meta.category = %v, want %v", frames[0].Meta["category"], core.CategoryText)
	}
	if frames[1].Meta["call_id"] != "c1" {
		t.Errorf("structured fields should be merged into _meta: %+v", frames[1].Meta)
	}
}

// TestNotifyProgressWithoutToken: without a subscription not a single frame is sent —
// the spec requires a notification to reference a token supplied by an active request, so inventing one
// means pushing into the void.
func TestNotifyProgressWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	notifyProgress(rec, core.NewProgressPump(nil), core.CategoryText, "a", nil)
	if rec.Body.Len() != 0 {
		t.Fatalf("nothing may be written without a subscription: %s", rec.Body.String())
	}
}
