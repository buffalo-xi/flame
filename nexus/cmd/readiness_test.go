package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Cause is the only field that may carry internal addresses (such as "get http://127.0.0.1:27081/...:
// http 500"), and /health serializes the whole readinessIssue to the probe. json:"-" is that gate — if it
// is casually changed, the internal topology leaks through the probe response and nothing else turns red.
func TestReadinessIssueCauseNotSerialized(t *testing.T) {
	issue := readinessIssue{
		Key:    "sandbox",
		Detail: "sandbox capability snapshot not fetched yet",
		Cause:  "get http://127.0.0.1:27081/v1/capabilities: http 500",
	}
	b, err := json.Marshal([]readinessIssue{issue})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "127.0.0.1") || strings.Contains(string(b), "capabilities") {
		t.Fatalf("Cause must not reach the response body, got %s", b)
	}

	// The probe must still see which dependency failed — dropping Cause must not drop Key/Detail with it.
	var back []readinessIssue
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 1 || back[0].Key != "sandbox" || back[0].Detail == "" {
		t.Errorf("the probe should receive dependency/detail, got %+v", back)
	}
}

// describeIssues is the log side: it must include Cause, otherwise debugging falls back to "which one
// failed and why is unknown".
func TestDescribeIssues(t *testing.T) {
	got := describeIssues([]readinessIssue{
		{Key: "db", Detail: "database not connected"},
		{Key: "sandbox", Detail: "snapshot not fetched yet", Cause: "http 500"},
	})
	want := "db: database not connected; sandbox: snapshot not fetched yet (http 500)"
	if got != want {
		t.Errorf("describeIssues = %q, want %q", got, want)
	}
	if got := describeIssues(nil); got != "" {
		t.Errorf("a ready service should produce no copy, got %q", got)
	}
}
