package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// Egress must survive the Wire / Spec round trip unchanged, all three states included - nil != false:
// the first means "not stated, follow the deployment default", the second "cut the network", and
// sandboxd treats them differently (mode=none rejects the latter, the former follows the config).
func TestWireSpecEgressRoundTrip(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name string
		in   *bool
	}{
		{"not stated", nil},
		{"egress on", ptr(true)},
		{"egress off", ptr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := (&Spec{Egress: tc.in}).Wire()
			if (w.Egress == nil) != (tc.in == nil) {
				t.Fatalf("nil semantics lost after Wire: in=%v out=%v", tc.in, w.Egress)
			}

			got := w.Spec()
			if (got.Egress == nil) != (tc.in == nil) {
				t.Fatalf("nil semantics lost after Spec: in=%v out=%v", tc.in, got.Egress)
			}
			if tc.in != nil && *got.Egress != *tc.in {
				t.Errorf("Egress = %v, want %v", *got.Egress, *tc.in)
			}
		})
	}
}

// With no statement the JSON must not carry an egress field: older sandboxd does not know it, and
// a stray false reads as "the caller explicitly asked for no network" in a capture or log.
func TestWireSpecEgressOmitEmpty(t *testing.T) {
	b, err := json.Marshal((&Spec{}).Wire())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "egress") {
		t.Errorf("no egress field expected when unstated: %s", b)
	}

	no := false
	b, err = json.Marshal((&Spec{Egress: &no}).Wire())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"egress":false`) {
		t.Errorf("explicit no-network must send false: %s", b)
	}
}
