package tools

import (
	"strings"
	"testing"
)

// The description of a resident tool (shell / browser) is the model's only clue for deciding "do I need a resident process":
// it must give both "when to use it" (one-shot means first) and "how to clean up afterwards". With only usage and no cost the
// model treats shell / browser as free exploration, leaving behind a resident process that holds resources without being used,
// every single turn.
//
// The assertions pin **identifiers** only (tool names / argument values: exec, kill, close, snapshot) plus "the description is
// long enough", never prose sentences: the description's language follows the product's primary language, and a Chinese sentence
// must not become part of the interface contract. Those two things (try one-shot means first, clean up afterwards) are exactly
// what these identifiers express.
func TestResidentToolDescriptionsMentionCost(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"shell", []string{"exec", "kill"}},
		{"browser", []string{"exec", "close", "snapshot"}},
	}
	for _, c := range cases {
		tool, ok := Get(c.name)
		if !ok {
			t.Fatalf("tool %s is not registered", c.name)
		}
		desc := tool.Meta().Description
		for _, w := range c.want {
			if !strings.Contains(desc, w) {
				t.Errorf("%s's description is missing %q:\n%s", c.name, w, desc)
			}
		}
		// Saying only "what it is" is not enough for the model to avoid the resident cost: both the cost and the cleanup have to
		// be mentioned
		if n := len([]rune(desc)); n < 150 {
			t.Errorf("%s's description is too short (%d characters); the resident cost and the cleanup action must be spelled out:\n%s", c.name, n, desc)
		}
	}
}
