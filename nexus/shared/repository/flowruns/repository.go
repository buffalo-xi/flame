package flowruns

import (
	"time"

	"flame/nexus/shared/models"
)

// Repository 工作流运行记录仓储接口
type Repository interface {
	// SaveRun 在事务内保存一次运行记录（run + 所有 node），
	// 并裁剪该工作流超出 maxRunRecords 的最旧记录（maxRunRecords <= 0 时不裁剪）。
	SaveRun(schema string, run *models.FlowRunRecord, nodes []*models.FlowNodeRecord, maxRunRecords int) error

	// ListRuns 列出某个工作流的历史运行记录（按开始时间倒序）。
	// runType 为空不过滤；status 为空不过滤；startFrom/startTo 为 nil 不过滤。返回记录列表与总数。
	ListRuns(schema string, flowId string, runType string, status string, startFrom, startTo *time.Time, limit int, offset int) ([]*models.FlowRunRecord, int, error)

	// GetRunDetail 获取一次运行记录及其所有节点记录（节点按开始时间升序）。
	GetRunDetail(schema string, runID string) (*models.FlowRunRecord, []*models.FlowNodeRecord, error)

	// DeleteRun 删除某工作流下的一次运行记录及其节点记录，返回删除的 run 条数。
	// 必须同时给出 flowId：只按 runId 删会让「删自己工作流的记录」变成「删任意记录」。
	DeleteRun(schema, flowId, runID string) (int64, error)

	// DeleteRuns 删除某工作流下的全部运行记录及其节点记录，返回删除的 run 条数。
	DeleteRuns(schema, flowId string) (int64, error)

	// DeleteRunsByFilter 按可选条件（工作流 / 状态 / 时间范围）批量删除运行记录，返回删除的 run 条数。
	// 无条件直接拒绝：管理端的批量清理不带筛选，一次误调用就是清库。
	DeleteRunsByFilter(schema, flowId, status string, startFrom, startTo *time.Time) (int64, error)
}

// NewRepository 创建运行记录存储实例
func NewRepository() Repository {
	return &PostgresRepository{}
}
