package assetpack

import (
	"context"
	"errors"
	"testing"
)

// A dependency on a custom tool travels with the package: the manifest carries a _deps/ prefix, the
// body and the attachments split cleanly, and install strips the prefix so attachments land in
// custom_tools/ rather than in the asset's own directory.
func TestPackFilesWithTreesBundlesToolAndSplitsCleanly(t *testing.T) {
	st := newStorage(t)
	ctx := context.Background()

	mustWrite(t, st, "tenants/t1/agents/poet/AGENT.md", "# poet\n")
	mustWrite(t, st, "tenants/t1/agents/poet/skills/verse/SKILL.md", "# verse\n")
	mustWrite(t, st, "tenants/t1/tools/weather/TOOL.json", `{"name":"weather"}`)
	mustWrite(t, st, "tenants/t1/tools/weather/main.py", "print('sun')\n")

	files, err := PackFilesWith(ctx, st, "tenants/t1/agents/poet", "platform-assets/a1/1", "AGENT.md",
		DefaultLimits(), PackOptions{Trees: []PackTree{{
			SrcDir:    "tenants/t1/tools/weather",
			DstPrefix: BundledToolsPrefix + "/weather",
			Required:  true,
		}}})
	if err != nil {
		t.Fatalf("packing failed: %v", err)
	}

	// Lexicographic by path ("_" sorts before "s"), so attachments come before skills/
	want := []string{
		"AGENT.md",
		BundledToolsPrefix + "/weather/TOOL.json",
		BundledToolsPrefix + "/weather/main.py",
		"skills/verse/SKILL.md",
	}
	if len(files) != len(want) {
		t.Fatalf("file count = %d, want %d: %+v", len(files), len(want), files)
	}
	for i, p := range want {
		if files[i].Path != p {
			t.Fatalf("entry %d = %s, want %s", i, files[i].Path, p)
		}
	}
	if got := mustRead(t, st, "platform-assets/a1/1/_deps/custom_tools/weather/main.py"); got != "print('sun')\n" {
		t.Fatalf("attachment snapshot content = %q", got)
	}

	body := BodyFiles(files)
	if len(body) != 2 || body[0].Path != "AGENT.md" || body[1].Path != "skills/verse/SKILL.md" {
		t.Fatalf("body split is wrong: %+v", body)
	}
	atts := Attachments(files)
	if len(atts) != 1 || len(atts["weather"]) != 2 {
		t.Fatalf("attachment split is wrong: %+v", atts)
	}

	if _, err := RestoreFilesUnder(ctx, st, "platform-assets/a1/1", "tenants/t2/tools/weather",
		ToolAttachmentPrefix("weather"), atts["weather"]); err != nil {
		t.Fatalf("restoring the attachment failed: %v", err)
	}
	if got := mustRead(t, st, "tenants/t2/tools/weather/main.py"); got != "print('sun')\n" {
		t.Fatalf("attachment written content = %q", got)
	}
	// Manifest entries outside the prefix must not be written by this path
	if ok, _ := st.Exists(ctx, "tenants/t2/tools/weather/AGENT.md"); ok {
		t.Fatal("a body file was dragged into the tool directory by the attachment restore")
	}
}

// A tree declared Required that does not exist must fail: the dependency said "the body travels
// along", and passing silently would ship a crippled package.
func TestPackFilesTreeRequiredMissingFails(t *testing.T) {
	st := newStorage(t)
	ctx := context.Background()
	mustWrite(t, st, "tenants/t1/skills/demo/SKILL.md", "# demo\n")

	_, err := PackFilesWith(ctx, st, "tenants/t1/skills/demo", "platform-assets/a1/1", "SKILL.md",
		DefaultLimits(), PackOptions{Trees: []PackTree{{
			SrcDir:    "tenants/t1/tools/nope",
			DstPrefix: BundledToolsPrefix + "/nope",
			Required:  true,
		}}})
	if !errors.Is(err, ErrNoFiles) {
		t.Fatalf("err = %v, want ErrNoFiles", err)
	}
}

// An attachment and a body file may share a name at different locations: the body is at the root, the
// attachment under _deps/, so the decision can only look at the prefix, never at the file name.
func TestBodyFilesKeepsLookalikeNames(t *testing.T) {
	files := []FileEntry{
		{Path: "TOOL.json"},
		{Path: BundledToolsPrefix + "/x/TOOL.json"},
		{Path: "_deps_other/README.md"},
	}
	body := BodyFiles(files)
	if len(body) != 2 {
		t.Fatalf("body entry count = %d, want 2: %+v", len(body), body)
	}
	if got := Attachments(files); len(got) != 1 || len(got["x"]) != 1 {
		t.Fatalf("attachment grouping is wrong: %+v", got)
	}
}
