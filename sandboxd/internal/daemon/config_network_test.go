package daemon

import (
	"io"
	"log"
	"strings"
	"testing"

	"flame/sandboxd/isolate"
	"flame/tools/xlog"
)

// silenceLogs mutes xlog until the test ends: the two "this deployment cannot do that" notices of on-request are logged
// at Error level, and leaving them in floods the test output.
func silenceLogs(t *testing.T) {
	t.Helper()
	oldInfo, oldErr := xlog.InfoLogger, xlog.ErrorLogger
	xlog.InfoLogger = log.New(io.Discard, "", 0)
	xlog.ErrorLogger = log.New(io.Discard, "", 0)
	t.Cleanup(func() { xlog.InfoLogger, xlog.ErrorLogger = oldInfo, oldErr })
}

// Each of the three tiers must be checked: off must really cut it off, on must really open it, and on-request must have
// **both states reachable** (stricter than the other two, hence a separate criterion).
func TestValidateNetworkPolicies(t *testing.T) {
	silenceLogs(t)

	cases := []struct {
		name    string
		cfg     Config
		wantErr string // empty = expect success
	}{
		{
			name: "off + docker none",
			cfg:  Config{Mode: isolate.DockerName, Network: NetworkOff, Docker: DockerConfig{Network: "none"}},
		},
		{
			name:    "off + docker bridge (cannot cut it off)",
			cfg:     Config{Mode: isolate.DockerName, Network: NetworkOff, Docker: DockerConfig{Network: "bridge"}},
			wantErr: "would still reach the network",
		},
		{
			name: "on + docker bridge",
			cfg:  Config{Mode: isolate.DockerName, Network: NetworkOn, Docker: DockerConfig{Network: "bridge"}},
		},
		{
			name:    "on + docker unset (would be filled with none)",
			cfg:     Config{Mode: isolate.DockerName, Network: NetworkOn},
			wantErr: "no network at all",
		},
		{
			name: "on-request + docker bridge",
			cfg:  Config{Mode: isolate.DockerName, Network: NetworkOnRequest, Docker: DockerConfig{Network: "bridge"}},
		},
		{
			name:    "on-request + docker none (no network to switch to for an egress request)",
			cfg:     Config{Mode: isolate.DockerName, Network: NetworkOnRequest, Docker: DockerConfig{Network: "none"}},
			wantErr: "no network to switch to",
		},
		{
			// Under mode=none cutting the network off is impossible, but an egress=false request can still be
			// rejected at runtime — so startup only warns, it does not block.
			name: "on-request + mode=none (warning only)",
			cfg:  Config{Mode: isolate.NoneName, Network: NetworkOnRequest},
		},
		{
			name:    "illegal value",
			cfg:     Config{Mode: isolate.DockerName, Network: "maybe", Docker: DockerConfig{Network: "bridge"}},
			wantErr: "unknown policy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validateNetwork()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("must not report an error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// A dirty value in networks must not be let through just because "the current mode did not select it"; conversely, when
// it is selected it must really override the top-level value.
func TestValidateNetworkModeOverride(t *testing.T) {
	silenceLogs(t)

	c := Config{
		Mode:     isolate.DockerName,
		Network:  NetworkOn,
		Networks: map[string]string{isolate.NoneName: "sometimes"},
		Docker:   DockerConfig{Network: "bridge"},
	}
	if err := c.validateNetwork(); err == nil || !strings.Contains(err.Error(), "networks.none") {
		t.Fatalf("an illegal value in networks should be rejected, got %v", err)
	}

	c = Config{
		Mode:     isolate.NoneName,
		Network:  NetworkOnRequest,
		Networks: map[string]string{isolate.NoneName: NetworkOn},
	}
	if got := c.EffectiveNetwork(); got != NetworkOn {
		t.Errorf("EffectiveNetwork = %q, want %q (networks takes priority by mode)", got, NetworkOn)
	}
	if err := c.validateNetwork(); err != nil {
		t.Errorf("after the override to on it should pass, got %v", err)
	}
}

// Under an isolated deployment /v1/code is always off and offers no config option — this pins "not a missing option but
// a deliberate refusal".
//
// The counter-argument: the pool is created globally per language, shared across tenants, and wire.Frame carries no
// tenant identity. Once it could be configured on, a single runaway tenant script could not be switched off alone —
// only rolled back platform-wide.
func TestScriptNetworkPolicy(t *testing.T) {
	silenceLogs(t)

	for _, policy := range []string{NetworkOff, NetworkOnRequest, NetworkOn} {
		c := Config{Mode: isolate.DockerName, Network: policy, Docker: DockerConfig{Network: "bridge"}}
		if got := c.EffectiveCodeNetwork(); got != NetworkOff {
			t.Errorf("with network=%s the script channel should still be off, got %q", policy, got)
		}
	}

	// mode=none is the exception: scripts run on a host process, so without an isolation unit there is no such thing as
	// cutting the network off. It must be reported honestly as on — reporting off would make the agent write code on a
	// wrong premise.
	c := Config{Mode: isolate.NoneName, Network: NetworkOnRequest}
	if got := c.EffectiveCodeNetwork(); got != NetworkOn {
		t.Errorf("with mode=none the script channel should honestly report %q (the host cannot cut it off), got %q", NetworkOn, got)
	}
}
