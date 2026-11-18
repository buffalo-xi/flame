package versioning

import (
	"context"
	"reflect"
	"testing"

	"flame/nexus/shared/storage"
)

// 载入旧 agent 版本时，草稿里有、版本里没有的私有技能必须被点名保留。
// 漏掉它们就会被当成「版本外的多余文件」删掉 —— 而它们是独立实体（skills 表有行），
// 删了目录就变成「有行、没正文」的空技能。
func TestKeptPrivateSkills(t *testing.T) {
	st := storage.NewFilesystemStorage(t.TempDir())
	ctx := context.Background()
	draft := "tenants/t0/agents/poet"

	for _, name := range []string{"verse", "lyric"} {
		key := draft + "/skills/" + name + "/SKILL.md"
		if err := st.WriteFile(ctx, key, []byte("# "+name)); err != nil {
			t.Fatalf("准备 %s 失败: %v", key, err)
		}
	}

	// 版本里只冻结了 verse
	got, err := KeptPrivateSkills(ctx, st, draft, []byte(`[{"name":"verse"}]`))
	if err != nil {
		t.Fatalf("KeptPrivateSkills 失败: %v", err)
	}
	if want := []string{"lyric"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KeptPrivateSkills = %v, want %v", got, want)
	}

	// skills 为空快照：两个都算「版本里没有」
	got, _ = KeptPrivateSkills(ctx, st, draft, []byte(`[]`))
	if want := []string{"lyric", "verse"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("空快照 KeptPrivateSkills = %v, want %v", got, want)
	}

	// 草稿压根没有 skills/ 目录：没有要保留的，也不该报错
	if got, err := KeptPrivateSkills(ctx, st, "tenants/t0/agents/none", []byte(`[]`)); err != nil || got != nil {
		t.Fatalf("无 skills 目录应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}
}
