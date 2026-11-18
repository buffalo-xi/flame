package agent

import (
	"context"
	"database/sql"
	"testing"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	flows "flame/nexus/shared/repository/flows"
)

// fakeFlowRepo 只关心 find 的分流：记录「按 id 查」收到了什么、名称查询被调了几次。
// 其余方法用不到，嵌入 nil 接口即可满足 flows.Repository。
type fakeFlowRepo struct {
	flows.Repository
	rootID   string
	getIDs   []string
	listHits int
}

func (f *fakeFlowRepo) GetFlow(_ *auth.AuthInfo, id string) (*models.Flow, error) {
	f.getIDs = append(f.getIDs, id)
	if id == f.rootID {
		return &models.Flow{ID: id, Name: "weekly-report"}, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeFlowRepo) ListFlows(_ *auth.AuthInfo, req *flows.ListFlowsRequest) ([]models.Flow, int, error) {
	f.listHits++
	return []models.Flow{{ID: f.rootID, Name: req.Name}}, 1, nil
}

// 名称绝不能被当成 id 塞进 `WHERE id = $1`：flows.id 是 uuid 列，Postgres 会报 22P02
// （invalid input syntax for type uuid）而不是「查无此行」，名称兜底就永远走不到。
// 工具层统一按 (ref, ref) 传参，所以 find 必须自己做形态判断。
func TestFindDoesNotTreatNameAsID(t *testing.T) {
	const id = "0190f000-0000-7000-8000-000000000001"
	repo := &fakeFlowRepo{rootID: id}
	m := &flowsManager{schema: "t0", userID: "u1", repo: repo}

	f, err := m.find(context.Background(), "weekly-report", "weekly-report")
	if err != nil {
		t.Fatalf("find = %v", err)
	}
	if f == nil || f.ID != id {
		t.Fatalf("按名称应命中 %s，实际 %+v", id, f)
	}
	for _, got := range repo.getIDs {
		if got == "weekly-report" {
			t.Fatalf("名称被当成 id 查了一次（getIDs = %v）", repo.getIDs)
		}
	}
	if repo.listHits != 1 {
		t.Errorf("名称查询次数 = %d, want 1", repo.listHits)
	}
}

// UUID 形态直接按 id 查，不该再扫一遍列表。
func TestFindUsesIDDirectly(t *testing.T) {
	const id = "0190f000-0000-7000-8000-000000000001"
	repo := &fakeFlowRepo{rootID: id}
	m := &flowsManager{schema: "t0", userID: "u1", repo: repo}

	f, err := m.find(context.Background(), id, id)
	if err != nil || f == nil || f.ID != id {
		t.Fatalf("find(id) = %+v, %v", f, err)
	}
	if repo.listHits != 0 {
		t.Errorf("id 命中时不该查名称列表，实际查了 %d 次", repo.listHits)
	}
}

// flow_run_records.id 同为 uuid 列：非 UUID 的 run_id 要当「不存在」，
// 不能把 22P02 的 SQL 报错抛给模型。
func TestGetRunTreatsNonUUIDAsNotFound(t *testing.T) {
	m := &flowsManager{schema: "t0", userID: "u1"}
	res, err := m.GetRun(context.Background(), "not-a-run-id")
	if err != nil {
		t.Fatalf("非 UUID run_id 不该报错: %v", err)
	}
	if res != nil {
		t.Fatalf("应视为不存在，实际 %+v", res)
	}
}
