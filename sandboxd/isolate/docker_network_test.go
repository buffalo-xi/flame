package isolate

import (
	"strings"
	"testing"

	"flame/tools/sandbox"
)

// containerArgs only translates, it does not make policy: the policy (deployment ceiling × request intent) has already
// been decided in sandboxd's handleExec, and this only translates "should this be offline" into --network.
//
// Start and Acquire share this one function, so this group of cases pins down both paths at once:
//   - simplex: egress takes effect per request;
//   - duplex: egress only takes effect when the unit is created (a later reuse cannot change it, and does not need to).
func TestDockerContainerArgsNetwork(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	s := &dockerSandbox{opts: Options{Network: "bridge"}}
	cases := []struct {
		name   string
		egress *bool
		want   string
	}{
		{"no statement → follow the config", nil, "bridge"},
		{"egress wanted → use the real configured network", ptr(true), "bridge"},
		{"explicitly offline → none", ptr(false), "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := s.containerArgs(&sandbox.Spec{Egress: tc.egress}, sandbox.Limits{})
			if !strings.Contains(strings.Join(args, " "), "--network "+tc.want) {
				t.Errorf("--network should be %q, actual args: %v", tc.want, args)
			}
		})
	}

	// When the config itself is none, egress=true can still only yield none — the backstop for a missed adjudication.
	// Better offline than unexpectedly online.
	s = &dockerSandbox{opts: Options{Network: "none"}}
	args := s.containerArgs(&sandbox.Spec{Egress: ptr(true)}, sandbox.Limits{})
	if !strings.Contains(strings.Join(args, " "), "--network none") {
		t.Errorf("a config of none must not grant egress, actual args: %v", args)
	}
}
