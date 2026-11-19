package sandbox

import (
	"context"
	"strings"
	"testing"
)

// The startup-intent check runs before Dial, so these cases need no running sandboxd.
//
// The criteria must match sandboxd's buildExecArgv: this side relaxes "Argv must not be empty" so
// the language path passes (argv is legitimately empty when Lang is set), not to loosen validation.
func TestRemoteStartIntentGuards(t *testing.T) {
	cases := []struct {
		name string
		spec *Spec
		want string
	}{
		{"both empty", &Spec{}, "empty argv"},
		{"language path without a script", &Spec{Lang: "python"}, "requires a script"},
		{
			"the language path and argv are mutually exclusive",
			&Spec{Lang: "python", Script: "/skill/main.py", Argv: []string{"lua", "/x.lua"}},
			"mutually exclusive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb, err := NewRemote(RemoteOptions{Endpoint: "ws://127.0.0.1:1/v1/exec"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = sb.Start(context.Background(), tc.spec)
			if err == nil {
				t.Fatal("should be rejected")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
