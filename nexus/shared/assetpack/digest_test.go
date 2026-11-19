package assetpack

import "testing"

// baseManifest is a minimal skill manifest: enough to compute a fingerprint, with no DB or storage
// dependency.
func baseManifest() Manifest {
	return Manifest{
		Kind:  KindSkill,
		Name:  "demo",
		Entry: "SKILL.md",
		Files: []FileEntry{
			{Path: "SKILL.md", Size: 10, SHA256: "aa"},
			{Path: "lib/a.js", Size: 4, SHA256: "bb"},
		},
	}
}

// Payloads with the same meaning but different bytes must produce the same fingerprint: the payload
// is a map, so an unstable key order is the norm.
func TestDigestIgnoresPayloadKeyOrder(t *testing.T) {
	a := baseManifest()
	b := baseManifest()
	if err := StampManifest(&a, []byte(`{"name":"demo","description":"d","n":1}`)); err != nil {
		t.Fatalf("stamp a: %v", err)
	}
	if err := StampManifest(&b, []byte(`{"n":1,"description":"d","name":"demo"}`)); err != nil {
		t.Fatalf("stamp b: %v", err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("the same content produced different fingerprints: %s vs %s", a.Digest, b.Digest)
	}
	if a.Spec != ManifestSpec {
		t.Fatalf("spec = %d, want %d", a.Spec, ManifestSpec)
	}
}

// A different file-manifest order is not a content change (directory traversal order is not
// guaranteed to be stable).
func TestDigestIgnoresFileOrder(t *testing.T) {
	a := baseManifest()
	b := baseManifest()
	b.Files[0], b.Files[1] = b.Files[1], b.Files[0]
	payload := []byte(`{"name":"demo"}`)
	if err := StampManifest(&a, payload); err != nil {
		t.Fatalf("stamp a: %v", err)
	}
	if err := StampManifest(&b, payload); err != nil {
		t.Fatalf("stamp b: %v", err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("only the order differs but the fingerprints do: %s vs %s", a.Digest, b.Digest)
	}
}

// Dependency declarations are sets semantically: listing the same set in another order does not
// change the content.
func TestDigestIgnoresDepsOrder(t *testing.T) {
	a := baseManifest()
	a.Deps = Deps{
		Tools:    []ToolDep{{Type: "tool", Ref: "custom:foo"}, {Type: "group", Ref: "grp"}},
		Skills:   []string{"s1", "s2"},
		Requires: []string{"sandbox.exec", "net.egress"},
	}
	b := baseManifest()
	b.Deps = Deps{
		Tools:    []ToolDep{{Type: "group", Ref: "grp"}, {Type: "tool", Ref: "custom:foo"}},
		Skills:   []string{"s2", "s1"},
		Requires: []string{"net.egress", "sandbox.exec"},
	}
	payload := []byte(`{"name":"demo"}`)
	if err := StampManifest(&a, payload); err != nil {
		t.Fatalf("stamp a: %v", err)
	}
	if err := StampManifest(&b, payload); err != nil {
		t.Fatalf("stamp b: %v", err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("the dependency order differs but the fingerprints do: %s vs %s", a.Digest, b.Digest)
	}
	// Sorting only happens on a copy: the caller's declaration order must not be rewritten
	if a.Deps.Tools[0].Ref != "custom:foo" || a.Deps.Skills[0] != "s1" {
		t.Fatalf("the manifest's dependency order was rewritten in place: %+v", a.Deps)
	}
}

// A real content change must change it: any difference in payload, dependencies or file digests.
func TestDigestChangesWithContent(t *testing.T) {
	payload := []byte(`{"name":"demo"}`)
	cases := []struct {
		name string
		mut  func(m *Manifest)
	}{
		{"payload", func(m *Manifest) { payload = []byte(`{"name":"demo","description":"x"}`) }},
		{"deps", func(m *Manifest) { m.Deps.Tools = []ToolDep{{Type: "tool", Ref: "custom:foo"}} }},
		{"file-sha", func(m *Manifest) { m.Files[0].SHA256 = "cc" }},
		{"file-size", func(m *Manifest) { m.Files[1].Size = 5 }},
	}

	base := baseManifest()
	if err := StampManifest(&base, payload); err != nil {
		t.Fatalf("stamp base: %v", err)
	}
	for _, c := range cases {
		m := baseManifest()
		c.mut(&m)
		if err := StampManifest(&m, payload); err != nil {
			t.Fatalf("%s: stamp: %v", c.name, err)
		}
		if m.Digest == base.Digest {
			t.Fatalf("%s changed but the fingerprint did not", c.name)
		}
	}
}

// An old package (no spec / digest) is still installable: Stamp only fills fields in and does not
// change existing semantics.
func TestStampKeepsExplicitSpec(t *testing.T) {
	m := baseManifest()
	m.Spec = 7
	if err := StampManifest(&m, []byte(`{}`)); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if m.Spec != 7 {
		t.Fatalf("spec was overwritten to %d, want 7 kept", m.Spec)
	}
}

func TestShortDigest(t *testing.T) {
	if got := ShortDigest("0123456789abcdef"); got != "01234567" {
		t.Fatalf("ShortDigest = %q", got)
	}
	if got := ShortDigest(""); got != "" {
		t.Fatalf("an empty fingerprint should be returned as is, got %q", got)
	}
}
