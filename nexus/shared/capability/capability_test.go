package capability

import "testing"

// 旧键必须被归一成新键：2026-10 把 sandbox.script 更名为 sandbox.code，
// 库里可能还留着旧行。归一失效的后果是**静默停电** —— 能力闸缺省拒绝，
// 工作流 code 节点直接报「平台未开通」，而管理员明明开着。
func TestNormalize(t *testing.T) {
	if got := Normalize(LegacyScript); got != Code {
		t.Errorf("Normalize(%q) = %q, want %q", LegacyScript, got, Code)
	}
	for _, key := range All() {
		if got := Normalize(key); got != key {
			t.Errorf("Normalize(%q) 不应改动新键，got %q", key, got)
		}
	}
	if got := Normalize("sandbox.unknown"); got != "sandbox.unknown" {
		t.Errorf("未知键应原样返回，got %q", got)
	}
}

// All() 只列新键：旧键若混进来，管理端会多出一行同名不同键的卡片。
func TestAllExcludesLegacy(t *testing.T) {
	for _, key := range All() {
		if key == LegacyScript {
			t.Fatalf("All() 不应包含旧键 %q", LegacyScript)
		}
	}
	var hasCode bool
	for _, key := range All() {
		if key == Code {
			hasCode = true
		}
	}
	if !hasCode {
		t.Errorf("All() 缺 %q（能力卡片不会列出这一行）", Code)
	}
}

// 过渡期 Valid 同时接受新旧键：前端从 All() 拿新键，但外部脚本可能还传旧键，
// 直接拒会变成一次无谓的 400。写入路径另有 Normalize 兜底，不会落旧键。
func TestValidAcceptBothKeys(t *testing.T) {
	if !Valid(Code) {
		t.Errorf("Valid(%q) = false", Code)
	}
	if !Valid(LegacyScript) {
		t.Errorf("Valid(%q) = false（过渡期应接受）", LegacyScript)
	}
	if Valid("sandbox.nope") {
		t.Error("未知键应被拒")
	}
}

// Label / ShortLabel 都要先归一：否则旧键会显示成裸键名，
// 管理员看到 "sandbox.script" 无法对应到界面上的「代码片段执行」。
func TestLabelNormalizesLegacy(t *testing.T) {
	if Label(LegacyScript) != Label(Code) {
		t.Errorf("Label(旧键) = %q，应与新键一致", Label(LegacyScript))
	}
	if ShortLabel(LegacyScript) != ShortLabel(Code) {
		t.Errorf("ShortLabel(旧键) = %q，应与新键一致", ShortLabel(LegacyScript))
	}
	if Label(Code) == Code {
		t.Errorf("Label(%q) 缺文案（界面会显示裸键名）", Code)
	}
	if ShortLabel(Code) == Code {
		t.Errorf("ShortLabel(%q) 缺文案", Code)
	}
}
