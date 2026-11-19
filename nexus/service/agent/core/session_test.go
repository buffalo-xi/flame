package core

import "testing"

// The session id only comes from params._meta: it is not a business argument of the endpoint.
// This group pins "what counts as given" and "what counts as not given".
func TestMetaSessionID(t *testing.T) {
	cases := []struct {
		name   string
		params any
		want   string
	}{
		{
			name:   "normal",
			params: map[string]any{"_meta": map[string]any{MetaSessionIDKey: " s-1 "}},
			want:   "s-1", // surrounding whitespace is trimmed
		},
		{
			name:   "no _meta",
			params: map[string]any{"name": "x"},
			want:   "",
		},
		{
			name:   "_meta is not an object",
			params: map[string]any{"_meta": "oops"},
			want:   "",
		},
		{
			name:   "_meta lacks the key",
			params: map[string]any{"_meta": map[string]any{"progressToken": "t"}},
			want:   "",
		},
		{
			name:   "wrong type (number) counts as not given",
			params: map[string]any{"_meta": map[string]any{MetaSessionIDKey: 42}},
			want:   "",
		},
		{
			name:   "an empty string counts as not given",
			params: map[string]any{"_meta": map[string]any{MetaSessionIDKey: "   "}},
			want:   "",
		},
		{
			// The legacy location (inside business arguments) does not count - falling back is the caller's job, and this only reads _meta
			name:   "in arguments only does not count",
			params: map[string]any{"arguments": map[string]any{"session_id": "s-1"}},
			want:   "",
		},
		{name: "params is not an object", params: "oops", want: ""},
		{name: "params is nil", params: nil, want: ""},
	}
	for _, c := range cases {
		if got := MetaSessionID(c.params); got != c.want {
			t.Errorf("%s: MetaSessionID = %q, want %q", c.name, got, c.want)
		}
	}
}

// Stripping has to satisfy all three at once: no session_id in the result, the original map untouched, and no copy when the key is
// absent.
func TestStripSessionID(t *testing.T) {
	orig := map[string]any{"q": "hi", "session_id": "s-1"}
	clean := StripSessionID(orig)

	if _, ok := clean["session_id"]; ok {
		t.Fatalf("session_id is still there after stripping: %v", clean)
	}
	if clean["q"] != "hi" {
		t.Fatalf("a business argument was lost: %v", clean)
	}
	if orig["session_id"] != "s-1" {
		t.Fatalf("the original map was modified: %v", orig)
	}

	// Without the key: returned as is (no map copy for the vast majority of requests)
	plain := map[string]any{"q": "hi"}
	if got := StripSessionID(plain); len(got) != 1 || got["q"] != "hi" {
		t.Fatalf("without session_id it should be returned as is: %v", got)
	}
	if StripSessionID(nil) != nil {
		t.Fatal("nil should return nil")
	}
}
