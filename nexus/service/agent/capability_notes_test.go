package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
)

// Cost reminder for resident tools (shell / browser): it only appears when they are granted, and it must give the "one-shot first,
// close when done" criterion.
// The assertions use keywords rather than whole sentences: the copy may change, the criterion (try exec first / kill|close when done)
// must not.
//
// The counter-example matters just as much: an agent granted only one-shot tools must not see this line - every unrelated rule added to
// the prompt is one more "should I be using tools at all" hesitation for the model.
func TestCapabilityNotesResidentTools(t *testing.T) {
	newImpl := func(allowed map[string]bool) *agentImpl {
		return &agentImpl{
			instance: &models.Agent{Status: models.StatusNormal},
			toolCtx:  &tools.Context{AllowedTools: allowed},
		}
	}

	shell := newImpl(map[string]bool{"exec": true, "shell": true}).capabilityNotes()
	for _, want := range []string{"resident process", "exec", "kill"} {
		if !strings.Contains(shell, want) {
			t.Errorf("granting shell should mention the resident cost and the closing action (missing %q):\n%s", want, shell)
		}
	}

	browser := newImpl(map[string]bool{"browser": true}).capabilityNotes()
	for _, want := range []string{"resident process", "close"} {
		if !strings.Contains(browser, want) {
			t.Errorf("granting browser should mention the resident cost and the closing action (missing %q):\n%s", want, browser)
		}
	}

	plain := newImpl(map[string]bool{"exec": true, "read_file": true}).capabilityNotes()
	if strings.Contains(plain, "resident process") {
		t.Errorf("no resident-tool reminder should appear without shell / browser granted:\n%s", plain)
	}
}
