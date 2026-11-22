package flowdoc

import "testing"

// The canvas submits a JSON string (the front end stringifies once) while tools write an
// object; both shapes must be recognised by every check.
func TestParseAcceptsBothShapes(t *testing.T) {
	const object = `{"nodes":[{"id":"1","data":{"label":"Start"}},{"id":"2","data":{"label":"Code"}}]}`
	const asString = `"{\"nodes\":[{\"id\":\"1\",\"data\":{\"label\":\"Start\"}},{\"id\":\"2\",\"data\":{\"label\":\"Code\"}}]}"`

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"object shape", object},
		{"string shape", asString},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, ok := Parse([]byte(tc.raw))
			if !ok {
				t.Fatal("the diagram was not recognised")
			}
			if got := doc.CountNodes(); got != 2 {
				t.Fatalf("node count = %d, want 2", got)
			}
		})
	}
}

// An empty or non-object diagram is a draft shape, not a write-boundary error: the run path
// reports it, the checks stay quiet.
func TestParseRejectsDraftShapes(t *testing.T) {
	for _, raw := range []string{"", "   ", "[]", `"not json"`, "null"} {
		if _, ok := Parse([]byte(raw)); ok {
			t.Errorf("%q was accepted, want a draft shape", raw)
		}
	}
}

// Comments are editor annotations: they never reach the executor and must not consume the
// node budget.
func TestCountNodesSkipsComments(t *testing.T) {
	doc, ok := Parse([]byte(`{"nodes":[
		{"id":"1","type":"comment","data":{"label":"note"}},
		{"id":"2","type":"start","data":{"label":"Start"}},
		{"id":"3","type":"code","data":{"label":"Code"}}
	]}`))
	if !ok {
		t.Fatal("the diagram was not recognised")
	}
	if got := doc.CountNodes(CommentNodeType); got != 2 {
		t.Fatalf("node count without comments = %d, want 2", got)
	}
	if got := doc.CountNodes(); got != 3 {
		t.Fatalf("node count including comments = %d, want 3", got)
	}
}

// A nil document (an unreadable diagram) counts as zero instead of panicking: the save point
// must never fail on a NilPointer when the shape is simply unknown.
func TestNilDocIsSafe(t *testing.T) {
	var doc *Doc
	if got := doc.CountNodes(CommentNodeType); got != 0 {
		t.Fatalf("nil node count = %d, want 0", got)
	}
	if dups := doc.DuplicateLabels(); dups != nil {
		t.Fatalf("nil duplicate labels = %v, want nil", dups)
	}
}

func TestDuplicateLabels(t *testing.T) {
	doc, ok := Parse([]byte(`{"nodes":[
		{"id":"2","data":{"label":"Code"}},
		{"id":"3","data":{"label":"Code"}},
		{"id":"4","data":{}},
		{"id":"5","data":{}}
	]}`))
	if !ok {
		t.Fatal("the diagram was not recognised")
	}
	dups := doc.DuplicateLabels()
	if len(dups) != 1 {
		t.Fatalf("duplicates = %v, want exactly one", dups)
	}
	if dups[0] != "Code (nodes 2 and 3)" {
		t.Fatalf("duplicate entry = %q", dups[0])
	}
}
