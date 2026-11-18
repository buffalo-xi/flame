package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListSvcEndpointsRequest 服务端点列表请求参数
type ListSvcEndpointsRequest struct {
	Page       int      `json:"page" form:"page"`
	Limit      int      `json:"limit" form:"limit"`
	Name       string   `json:"name" form:"name"`
	TargetType string   `json:"target_type" form:"target_type"`
	TargetID   string   `json:"target_id" form:"target_id"`
	Protocol   string   `json:"protocol" form:"protocol"`
	Exposed    *bool    `json:"exposed" form:"exposed"`
	SortBy     []string `json:"sort_by" form:"sort_by"`
	SortOrder  []string `json:"sort_order" form:"sort_order"`
}

// SvcEndpointRepository 服务端点仓库接口
type SvcEndpointRepository interface {
	// GetByID 根据ID获取服务端点
	GetByID(authInfo *auth.AuthInfo, id string) (*models.SvcEndpoint, error)

	// FindByName 按 protocol + name 精确查端点（对外调用按名定位用，大小写不敏感）；
	// 不存在返回 (nil, nil)。
	FindByName(authInfo *auth.AuthInfo, protocol, name string) (*models.SvcEndpoint, error)

	// Create 创建服务端点
	Create(authInfo *auth.AuthInfo, svcEndpoint *models.SvcEndpoint) (string, error)

	// Update 更新服务端点
	Update(authInfo *auth.AuthInfo, id string, svcEndpoint *models.SvcEndpoint) error

	// UpdateExposed 更新端点的对外状态（true=对外开放，false=仅内部）
	UpdateExposed(authInfo *auth.AuthInfo, id string, exposed bool) error

	// Delete 根据ID删除单个服务端点
	Delete(authInfo *auth.AuthInfo, id string) error

	// DeleteByTarget 删除指定目标的所有协议端点，并清除目标上的端点标记
	DeleteByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error

	// RetractByTarget 收回指定目标所有协议端点的对外（仅置 exposed=false，端点记录与配置保留）
	RetractByTarget(authInfo *auth.AuthInfo, targetType string, targetID string) error

	// List 获取服务端点列表
	List(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, int, error)

	// ListAll 取出满足条件的全部端点（不分页、不统计总数）。
	// 「要把整份目录装进内存」的场景（MCP / RPC 的工具注册）必须用它：
	// List 有默认 limit=10，会静默截断。
	ListAll(authInfo *auth.AuthInfo, req *ListSvcEndpointsRequest) ([]models.SvcEndpoint, error)
}
