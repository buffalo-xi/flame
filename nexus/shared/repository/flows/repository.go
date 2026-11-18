package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// ListFlowsRequest 工作流实例列表请求参数
type ListFlowsRequest struct {
	Page      int      `json:"page"`
	Limit     int      `json:"limit"`
	Name      string   `json:"name"`
	SortBy    []string `json:"sort_by"`
	SortOrder []string `json:"sort_order"`
}

// Repository 工作流实例存储接口
type Repository interface {
	// ListFlows 获取所有工作流实例
	ListFlows(authInfo *auth.AuthInfo, req *ListFlowsRequest) ([]models.Flow, int, error)
	// GetFlow 根据ID获取工作流实例
	GetFlow(authInfo *auth.AuthInfo, id string) (*models.Flow, error)
	// ExistsFlowName 判断同租户下是否已存在同名工作流（agent 重名校验用）
	ExistsFlowName(authInfo *auth.AuthInfo, name string) (bool, error)
	// FindByName 按名字取一行（重名判断与「同名覆盖安装」用）。不存在返回 (nil, nil)。
	FindByName(authInfo *auth.AuthInfo, name string) (*models.Flow, error)
	// CreateFlow 创建工作流实例
	CreateFlow(authInfo *auth.AuthInfo, Instance *models.Flow) (string, error)
	// UpdateFlow 更新工作流实例
	UpdateFlow(authInfo *auth.AuthInfo, id string, Instance *models.Flow) error
	// UpdateFlowDiagram 只替换 diagram（边工具用）：不碰名称/描述/限额/断点
	UpdateFlowDiagram(authInfo *auth.AuthInfo, id string, diagram xsql.JSONB) error
	// UpdateFlowArguments 只替换 arguments（入口参数声明）：不碰名称/描述/diagram/限额/断点，
	// 并在同一事务里把 svc_endpoints.input_schema 同步过去
	UpdateFlowArguments(authInfo *auth.AuthInfo, id string, arguments xsql.JSONB) error
	// DeleteFlow 删除工作流实例
	DeleteFlow(authInfo *auth.AuthInfo, id string) error
	// ListBreakpoints 查询工作流断点 node_id 列表
	ListBreakpoints(authInfo *auth.AuthInfo, flowId string) ([]string, error)
	// SetBreakpoint 单条增删断点（运行中即时落库，防刷新丢失）
	SetBreakpoint(authInfo *auth.AuthInfo, flowId string, nodeID string, on bool) error
	// UpdateStatus 仅更新 status（1=active 启用 | 2=disabled 停用）
	UpdateStatus(authInfo *auth.AuthInfo, id string, status int) error

	// ListFlowVersions 版本历史（新到旧，不含 diagram）
	ListFlowVersions(authInfo *auth.AuthInfo, flowID string) ([]FlowVersion, error)
	// ReleaseFlowVersion 把当前工作副本冻结成新版本并设为当前
	ReleaseFlowVersion(authInfo *auth.AuthInfo, flowID string, note string) (*FlowVersion, error)
	// RollbackFlowVersion 把「正在使用的版本」切到历史版本（纯指针，不发新版本，不动草稿）
	RollbackFlowVersion(authInfo *auth.AuthInfo, flowID string, version int) (*FlowVersion, error)
	// ApplyFlowVersionToDraft 把历史版本内容载入工作副本（不发布，线上不变）
	ApplyFlowVersionToDraft(authInfo *auth.AuthInfo, flowID string, version int) error
	// DeleteFlowVersion 删除历史版本（当前版本不可删，线上与草稿都不动）
	DeleteFlowVersion(authInfo *auth.AuthInfo, flowID string, version int) error
	// GetCurrentFlowVersion 取正式版本的定义：正式入口（MCP/RPC/端点/flow 直调）走这里；
	// 没有正式版本返回 ErrNoReleasedVersion，调用方应拒绝执行而不是退回草稿
	GetCurrentFlowVersion(authInfo *auth.AuthInfo, id string) (*models.Flow, error)
}
