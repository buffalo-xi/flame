package gw

import (
	"context"
	"errors"
	"testing"

	"flame/tools/xregistry"
)

// fakeDiscovery answers with a fixed list (or a fixed error).
type fakeDiscovery struct {
	insts []xregistry.Instance
	err   error
}

func (f *fakeDiscovery) ListInstances(context.Context, string) ([]xregistry.Instance, error) {
	return f.insts, f.err
}

// newDiscoveryGateway builds a gateway around one static backend: enough for discoverOnce, which needs the pool, the
// config and a sticky store (bindings are cleared for evicted backends).
func newDiscoveryGateway(t *testing.T, disc xregistry.Discovery) *Gateway {
	t.Helper()
	return &Gateway{
		cfg:       &Config{Discovery: DiscoveryConfig{ServiceName: "sandboxd", IntervalSec: 5}},
		pool:      NewPool([]BackendConfig{{URL: "http://127.0.0.1:27081"}}),
		sticky:    NewMemorySticky(),
		discovery: disc,
	}
}

func poolURLs(p *Pool) []string {
	bs := p.all()
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.URL)
	}
	return out
}

// TestDiscoverOnceKeepsPoolOnFailedPull: a registry blip must not take the gateway's backends away.
func TestDiscoverOnceKeepsPoolOnFailedPull(t *testing.T) {
	g := newDiscoveryGateway(t, &fakeDiscovery{err: errors.New("registry unreachable")})
	g.discoverOnce(context.Background())

	if got := poolURLs(g.pool); len(got) != 1 || got[0] != "http://127.0.0.1:27081" {
		t.Fatalf("pool = %v, want the static seed kept", got)
	}
}

// TestDiscoverOnceKeepsPoolOnEmptyAnswer: a successful but empty answer is ambiguous - "the last instance deregistered" and
// "the registry answered before the service re-registered" (a rolling restart) look identical - so it must not empty the
// pool either. A backend that is really gone is evicted by the health check instead.
func TestDiscoverOnceKeepsPoolOnEmptyAnswer(t *testing.T) {
	g := newDiscoveryGateway(t, &fakeDiscovery{})
	g.discoverOnce(context.Background())

	if got := poolURLs(g.pool); len(got) != 1 || got[0] != "http://127.0.0.1:27081" {
		t.Fatalf("pool = %v, want the current pool kept on an empty answer", got)
	}
	if !g.discoveryEmptyLogged {
		t.Error("the empty answer must be reported, otherwise the pool silently stops following the registry")
	}
}

// TestDiscoverOnceRegistryIsAuthoritative: a non-empty answer is the truth, so a static seed the registry does not list
// leaves the pool - otherwise an instance the operator already deregistered would keep serving traffic.
func TestDiscoverOnceRegistryIsAuthoritative(t *testing.T) {
	g := newDiscoveryGateway(t, &fakeDiscovery{insts: []xregistry.Instance{{Address: "10.0.0.2", Port: 27081}}})
	g.discoverOnce(context.Background())

	if got := poolURLs(g.pool); len(got) != 1 || got[0] != "http://10.0.0.2:27081" {
		t.Fatalf("pool = %v, want exactly the registered instance", got)
	}
}

// TestDiscoverOnceRecoversFromEmptyAnswer: the "already reported" flag must not latch, or the next real list would be
// applied without a word and the empty-state log would never come back for a later streak.
func TestDiscoverOnceRecoversFromEmptyAnswer(t *testing.T) {
	disc := &fakeDiscovery{}
	g := newDiscoveryGateway(t, disc)
	g.discoverOnce(context.Background())
	if !g.discoveryEmptyLogged {
		t.Fatal("the first empty answer must be reported")
	}

	disc.insts = []xregistry.Instance{{Address: "10.0.0.3", Port: 27081}}
	g.discoverOnce(context.Background())
	if g.discoveryEmptyLogged {
		t.Error("the empty streak flag must reset once the registry answers again")
	}
	if got := poolURLs(g.pool); len(got) != 1 || got[0] != "http://10.0.0.3:27081" {
		t.Fatalf("pool = %v, want the registry list applied after the empty answer", got)
	}
}
