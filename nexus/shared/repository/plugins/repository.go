// Package plugins 租户级插件配置的存取（表 t0.plugins）。
//
// 与 mcpconnections 的仓储同形状，但更窄：**插件本体不在库里** ——
// 工具面与实现来自磁盘（{root_dir}/plugins），这里只记「本租户启用哪个插件、
// 用哪个实现、怎么配」。
//
// 行标识是 (kind, name) 而不是单独的 name：不同种类的插件是独立命名空间，
// 将来 sources 与 tools 同名时不能互相覆盖。绝大多数调用方手里都有 manifest，
// kind 直接取 manifest.Kind 即可，不要在这里凭空写字符串。
package plugins

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

const (
	// KindTools 工具插件。当前唯一支持的插件类型（与 service/plugin.KindTools、
	// pluginpolicy.KindTools 同值；三处分属不同层，故各自声明而不是互相 import）。
	KindTools = "tools"

	// tableName 租户级插件配置表名。集中一处：将来改动只动这里。
	// 结构见 nexus/sql/plugins.sql 与 platform/provision/tenant_template.sql。
	tableName = "plugins"
)

// Repository 插件配置仓储。
type Repository interface {
	// Get 按 (kind, name) 取；不存在返回 sql.ErrNoRows。
	Get(authInfo *auth.AuthInfo, kind, name string) (*models.Plugin, error)

	// List 列出本租户已配置的全部插件（含停用、含全部 kind）。
	// 不分 kind 是刻意的：管理端与平台矩阵都要「一次取全」，逐个 kind 查会打出 N 条 SQL。
	List(authInfo *auth.AuthInfo) ([]models.Plugin, error)

	// Upsert 按 (kind, name) 插入或更新（覆盖 impl / fallbacks / configs / is_active）。
	// p.Kind 为空时直接报错，**不**回退成默认值 —— 静默默认会把行写到读不到的地方。
	Upsert(authInfo *auth.AuthInfo, p *models.Plugin) error

	// SetActive 只改启用状态；不存在返回 sql.ErrNoRows。
	SetActive(authInfo *auth.AuthInfo, kind, name string, active bool) error

	// Delete 移除本租户的插件配置（磁盘上的插件不受影响）。
	Delete(authInfo *auth.AuthInfo, kind, name string) error

	// TouchUsed 记一次成功调用（顺带清空上一次错误）。行已不存在时静默成功。
	TouchUsed(authInfo *auth.AuthInfo, kind, name string) error

	// SetError 记一次失败的一句话原因（实现方负责截断）。行已不存在时静默成功。
	SetError(authInfo *auth.AuthInfo, kind, name, message string) error
}
