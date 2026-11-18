package assetpack

import (
	"context"
	"errors"
	"testing"
)

// 依赖的自定义工具要跟着包走：清单带 _deps/ 前缀，本体与附件能干净拆开，
// 安装时附件剥掉前缀落进 custom_tools/ 而不是资产自己的目录。
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
		t.Fatalf("打包失败: %v", err)
	}

	// 按 path 字典序（"_" 在 "s" 前），附件排在 skills/ 之前
	want := []string{
		"AGENT.md",
		BundledToolsPrefix + "/weather/TOOL.json",
		BundledToolsPrefix + "/weather/main.py",
		"skills/verse/SKILL.md",
	}
	if len(files) != len(want) {
		t.Fatalf("文件数 = %d, 期望 %d: %+v", len(files), len(want), files)
	}
	for i, p := range want {
		if files[i].Path != p {
			t.Fatalf("第 %d 项 = %s, 期望 %s", i, files[i].Path, p)
		}
	}
	if got := mustRead(t, st, "platform-assets/a1/1/_deps/custom_tools/weather/main.py"); got != "print('sun')\n" {
		t.Fatalf("附件快照内容 = %q", got)
	}

	body := BodyFiles(files)
	if len(body) != 2 || body[0].Path != "AGENT.md" || body[1].Path != "skills/verse/SKILL.md" {
		t.Fatalf("本体拆分不对: %+v", body)
	}
	atts := Attachments(files)
	if len(atts) != 1 || len(atts["weather"]) != 2 {
		t.Fatalf("附件拆分不对: %+v", atts)
	}

	if _, err := RestoreFilesUnder(ctx, st, "platform-assets/a1/1", "tenants/t2/tools/weather",
		ToolAttachmentPrefix("weather"), atts["weather"]); err != nil {
		t.Fatalf("还原附件失败: %v", err)
	}
	if got := mustRead(t, st, "tenants/t2/tools/weather/main.py"); got != "print('sun')\n" {
		t.Fatalf("附件落盘内容 = %q", got)
	}
	// 前缀之外的清单项不该被这条路径落盘
	if ok, _ := st.Exists(ctx, "tenants/t2/tools/weather/AGENT.md"); ok {
		t.Fatal("本体文件被附件还原带进了工具目录")
	}
}

// 声明为 Required 的附件目录不存在要报错：依赖里写了「要带本体」，静默放过就是发了个残包。
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
		t.Fatalf("err = %v, 期望 ErrNoFiles", err)
	}
}

// 附件与本体同名不同位：本体在根、附件在 _deps/，判定只能看前缀，不能看文件名。
func TestBodyFilesKeepsLookalikeNames(t *testing.T) {
	files := []FileEntry{
		{Path: "TOOL.json"},
		{Path: BundledToolsPrefix + "/x/TOOL.json"},
		{Path: "_deps_other/README.md"},
	}
	body := BodyFiles(files)
	if len(body) != 2 {
		t.Fatalf("本体项数 = %d, 期望 2: %+v", len(body), body)
	}
	if got := Attachments(files); len(got) != 1 || len(got["x"]) != 1 {
		t.Fatalf("附件分组不对: %+v", got)
	}
}
