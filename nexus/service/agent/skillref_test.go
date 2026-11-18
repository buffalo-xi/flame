package agent

import (
	"strings"
	"testing"

	"flame/nexus/service/agent/tools"
)

// skill_id 里填了名字（或任何非 UUID）：必须当场给出「按名字请用 name」的指引，
// 而不是把 Postgres 的 22P02（invalid input syntax for type uuid）糊给模型。
// skills.id 是 uuid 列，直接拿名字去查就是一条 SQL 报错，对模型零指导意义。
func TestFindRefRejectsNonUUIDSkillID(t *testing.T) {
	m := &skillManager{schema: "t0", agentID: "a1", isSupervisor: true}

	_, err := m.findRef(tools.SkillRef{ID: "english-joke-writing"})
	if err == nil {
		t.Fatal("非 UUID 的 skill_id 应该报错")
	}
	for _, want := range []string{"english-joke-writing", "name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err 缺少 %q: %v", want, err)
		}
	}
}
