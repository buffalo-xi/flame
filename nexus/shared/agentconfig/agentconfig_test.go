package agentconfig

import (
	"database/sql"
	"testing"
)

func null(v int) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }

// resolveField 是全部「租户 / 系统 / fallback」语义的唯一实现，逐条钉住：
// 表没建、配得比上级高、写了 -1 或 0 —— 这些真实会发生的输入都必须收敛到正数。
func TestResolveField(t *testing.T) {
	const fb = 80
	tests := []struct {
		name   string
		tenant sql.NullInt32
		sys    sql.NullInt32
		want   int
	}{
		{"都没配 → fallback", sql.NullInt32{}, sql.NullInt32{}, fb},
		{"只有系统行 → 系统值", sql.NullInt32{}, null(100), 100},
		{"租户低于系统 → 租户值", null(40), null(100), 40},
		{"租户高于系统 → 被截到系统值（系统行是硬顶）", null(500), null(100), 100},
		{"只有租户行 → 租户值（系统行未设置时 fallback 当顶）", null(40), sql.NullInt32{}, 40},
		{"租户 -1 → fallback（本表不接受「不限制」）", null(-1), sql.NullInt32{}, fb},
		{"租户 0 → fallback（直改库写出的怪值）", null(0), sql.NullInt32{}, fb},
		{"系统 -1 → 视为未设置，租户值照用", null(40), null(-1), 40},
		{"系统 0 → 视为未设置，租户值照用", null(40), null(0), 40},
		{"系统 -1 且租户未设置 → fallback", sql.NullInt32{}, null(-1), fb},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveField(tc.tenant, tc.sys, fb); got != tc.want {
				t.Errorf("resolveField = %d, want %d", got, tc.want)
			}
		})
	}
}

// fallback 必须与改造前的硬编码常量逐一相等：
// 表没建时行为要和今天完全一致，否则「加了个配置」反而变成一次静默的行为变更。
func TestFallbackMatchesLegacyConstants(t *testing.T) {
	fb := Fallback()
	if fb.MaxIterations != 80 {
		t.Errorf("MaxIterations fallback = %d, want 80（原 agent_impl.maxIterations）", fb.MaxIterations)
	}
	if fb.MaxCallDepth != 3 {
		t.Errorf("MaxCallDepth fallback = %d, want 3（原 caller.maxCallDepth）", fb.MaxCallDepth)
	}
	if fb.MaxTools != 50 {
		t.Errorf("MaxTools fallback = %d, want 50（原 toolgrant.DefaultMaxTools）", fb.MaxTools)
	}
	if fb.MaxOutputBytes != 64<<10 {
		t.Errorf("MaxOutputBytes fallback = %d, want 65536（原 tools.DefaultMaxOutput）", fb.MaxOutputBytes)
	}
}
