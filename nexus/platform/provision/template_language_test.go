package provision

import (
	"io/fs"
	"strings"
	"testing"
	"unicode"
)

// systemEntityDirs 是「系统实体」在模板内的定义域目录。
//
// 名单与 shared/models/protected.go 的 protectedAgents / protectedSkills 保持同步：
// main + skill-authoring / agent-authoring / custom-tool-authoring / flow-authoring。
// 这些文件随产物发布、直接进模型上下文，必须英文（见仓库根 AGENTS.md
// 「系统定义域与内置工具文案必须英文」）。加保护名单时这里要一起加。
var systemEntityDirs = []string{
	"template/files/agents/main",
	"template/files/skills/skill-authoring",
	"template/files/skills/agent-authoring",
	"template/files/skills/custom-tool-authoring",
	"template/files/skills/flow-authoring",
}

// TestSystemEntityFilesAreEnglish 把「系统实体文件必须英文」变成 CI 闸门。
//
// 只查模板（embed FS），不查 tenants/ 下的租户目录：租户自建的 agent / skill 跟随用户语言，
// 历史版本快照也不该被篡改。这条测试防的是整批翻译或后续改动里混回中文
// （提示词语言一变，模型行为会随语言环境分叉）。
func TestSystemEntityFilesAreEnglish(t *testing.T) {
	matched := 0
	err := fs.WalkDir(templateAssets, "template/files", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !underAnyDir(path, systemEntityDirs) {
			return nil
		}
		matched++
		b, err := fs.ReadFile(templateAssets, path)
		if err != nil {
			return err
		}
		line := 1
		for _, r := range string(b) {
			if r == '\n' {
				line++
				continue
			}
			if unicode.Is(unicode.Han, r) {
				t.Errorf("%s:%d 出现中文：系统实体的定义域文件必须英文（见 AGENTS.md）", path, line)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	// 目录被改名 / 删除时不能静默通过，否则闸门形同虚设
	if matched == 0 {
		t.Fatalf("没有匹配到任何系统实体文件，检查 systemEntityDirs 是否已过期")
	}
}

func underAnyDir(path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}
