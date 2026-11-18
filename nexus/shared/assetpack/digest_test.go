package assetpack

import "testing"

// baseManifest 一份最小的技能清单：够算指纹，不依赖任何 DB / storage。
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

// 语义相同、字节不同的 payload 必须得到同一个指纹：payload 是 map，键序不稳定是常态。
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
		t.Fatalf("同一份内容得到不同指纹：%s vs %s", a.Digest, b.Digest)
	}
	if a.Spec != ManifestSpec {
		t.Fatalf("spec = %d，期望 %d", a.Spec, ManifestSpec)
	}
}

// 文件清单顺序不同不算内容变化（目录遍历顺序不保证稳定）。
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
		t.Fatalf("仅顺序不同却得到不同指纹：%s vs %s", a.Digest, b.Digest)
	}
}

// 依赖声明语义上是集合：同一批依赖换个书写顺序，内容并未改变。
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
		t.Fatalf("依赖顺序不同却得到不同指纹：%s vs %s", a.Digest, b.Digest)
	}
	// 排序只发生在副本上：不能把调用方的声明顺序改掉。
	if a.Deps.Tools[0].Ref != "custom:foo" || a.Deps.Skills[0] != "s1" {
		t.Fatalf("manifest 的依赖顺序被就地改写了：%+v", a.Deps)
	}
}

// 内容真的变了就必须变：payload、依赖、文件摘要任何一个改动都要换指纹。
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
			t.Fatalf("%s 变了但指纹没变", c.name)
		}
	}
}

// 老包（没有 spec / digest）仍然能装：Stamp 只是补字段，不改变已有语义。
func TestStampKeepsExplicitSpec(t *testing.T) {
	m := baseManifest()
	m.Spec = 7
	if err := StampManifest(&m, []byte(`{}`)); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if m.Spec != 7 {
		t.Fatalf("spec 被覆盖成 %d，期望保留 7", m.Spec)
	}
}

func TestShortDigest(t *testing.T) {
	if got := ShortDigest("0123456789abcdef"); got != "01234567" {
		t.Fatalf("ShortDigest = %q", got)
	}
	if got := ShortDigest(""); got != "" {
		t.Fatalf("空指纹应原样返回，得到 %q", got)
	}
}
