package daemon

import (
	"strings"
	"testing"

	"flame/sandboxd/isolate"
	"flame/tools/sandbox"
)

// resolveExecNetwork is the decision point of the "deployment ceiling × request intent" layer.
// The tenant grant is not here (nexus only sends egress=true once it has been evaluated), so the input is one of
// exactly three:
//
//	nil   = no opinion expressed (older nexus / a non-exec channel)
//	false = explicitly wants offline
//	true  = wants egress
func TestResolveExecNetwork(t *testing.T) {
	silenceLogs(t)

	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name    string
		policy  string
		mode    string
		in      *bool // request intent
		want    *bool // spec.Egress written back by the decision (nil = leave it to isolate and the config)
		wantErr string
	}{
		// off: a deployment-level hard gate, nobody gets egress.
		{"off + no opinion → follow config (offline)", NetworkOff, isolate.DockerName, nil, nil, ""},
		{"off + explicitly offline", NetworkOff, isolate.DockerName, ptr(false), nil, ""},
		{"off + wants egress → refused", NetworkOff, isolate.DockerName, ptr(true), nil, "network=off"},

		// on: always online, a request's false does not change the outcome (the control belongs to the deployment).
		{"on + no opinion → follow config (online)", NetworkOn, isolate.DockerName, nil, nil, ""},
		{"on + wants offline is ignored", NetworkOn, isolate.DockerName, ptr(false), nil, ""},

		// on-request: decided per request, offline by default.
		{"on-request + no opinion → offline", NetworkOnRequest, isolate.DockerName, nil, ptr(false), ""},
		{"on-request + explicitly offline", NetworkOnRequest, isolate.DockerName, ptr(false), ptr(false), ""},
		{"on-request + wants egress", NetworkOnRequest, isolate.DockerName, ptr(true), ptr(true), ""},

		// mode=none: a host process cannot be cut off. Requests wanting egress are allowed (it already has it),
		// requests wanting offline can only be refused — silently running online would be a lie.
		{"on-request + none + wants egress", NetworkOnRequest, isolate.NoneName, ptr(true), ptr(true), ""},
		{"on-request + none + no opinion → refused", NetworkOnRequest, isolate.NoneName, nil, nil, "mode=none"},
		{"on-request + none + explicitly offline → refused", NetworkOnRequest, isolate.NoneName, ptr(false), nil, "mode=none"},

		{"unknown policy", "maybe", isolate.DockerName, nil, nil, "unknown policy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &Config{Mode: tc.mode, Network: tc.policy}}
			spec := &sandbox.Spec{Egress: tc.in}

			err := s.resolveExecNetwork(spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("must not error, got %v", err)
			}
			switch {
			case tc.want == nil && spec.Egress != nil:
				t.Errorf("spec.Egress should stay nil (left to isolate and the config), got %v", *spec.Egress)
			case tc.want != nil && spec.Egress == nil:
				t.Errorf("spec.Egress should be decided as %v, got nil", *tc.want)
			case tc.want != nil && *spec.Egress != *tc.want:
				t.Errorf("spec.Egress = %v, want %v", *spec.Egress, *tc.want)
			}
		})
	}
}
