// Package pluginpolicy 提供「租户 × 插件」的平台级策略：能否使用。
//
// 与 {schema}.plugins 的分工（两者都要过，缺一不可）：
//
//	platform.tenant_plugins   平台侧：这个租户能不能用 —— 平台可写
//	{schema}.plugins          租户侧：用哪个实现、key 是什么、是否启用 —— 租户可写
//
// 为什么授权不放 {schema}.plugins：那张表的写入口在租户管理端，
// 「能不能用」是成本与安全护栏，租户能自己开就等于没有护栏。
//
// kind：两张表的主键都含 kind（当前恒为 tools）。本包对外签名仍是「单个名字」——
// 它的调用方（Runner / 工具目录 / 管理端）全部只服务工具插件，把 kind 提到参数上
// 等于给没人用的维度开一个口。将来 sources 需要平台授权时，再加 ResolveKind 之类的
// 变体，现有函数保持不动即可。
//
// 语义是**两层**（缺省开放 × 全局闸）：
//
//		生效 = (租户行 ?? 开放) AND NOT blocked
//
//	  - **缺省开放**：没有行 = 可以用，平台不再逐个租户开通。插件本体是平台放进
//	    磁盘的可信代码，而凭证（api_key 等）在 {schema}.plugins.configs 里由租户
//	    自己填、成本也落在租户侧，「能不能用」不再是成本护栏而是产品开关。
//	  - 租户行现在只表示**停用**（is_allowed = false）：只关掉某租户的某个插件，
//	    比全局闸波及所有人更细。租户侧仍需自己启用 + 选实现，见 {schema}.plugins。
//	  - 全局闸（platform.plugin_gates）仍是**急停**：缺省放行（没有行 = 不拦），
//	    一旦拦下压过租户的显式开放。它不是「第三层回退」—— 回退链遇到有值的层就停，
//	    而硬闸必须能压过一切，所以是最后乘上去的 0，不是链上的一环。
//	  - 2026-10-21 移除了「系统默认行」（tenant_id = ''）：它只是个隐式缺省值，
//	    和急停是两种东西，放在同一张表里会让管理员分不清「我关的」和「本来就没有」，
//	    也会误以为它是总闸。库里已用 CHECK (tenant_id <> '') 封死。
//	  - 2026-11-02 缺省从「拒绝」翻成「开放」（产品决定）：租户只要在自己的插件页
//	    启用并选实现就能用，平台只在需要禁用某个租户时介入。
//
// 仍然成立的边界：全局闸必须能压过一切；读策略失败时调用方 fail-closed
// （见 Runner），否则一次 DB 抖动会把「已停用」读成「默认开放」。
//
// 历史：本包曾带 daily_limit / concurrency_limit，但配额计数只在进程内
// （重启清零、多副本各算各的，「每天最多 N 次」名不副实），已移除。
// 将来要做真正的配额，应基于共享存储（Redis）重新设计，而不是恢复这两列。
//
// 不做缓存：一次插件调用只多一条按主键的查询，而缓存会引入「平台改完授权、
// 租户要等 TTL 才生效」的口径问题 —— 这里优先正确性。急停场景下这一点是硬要求。
package pluginpolicy

import (
	"database/sql"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
)

// KindTools 工具插件。当前唯一被平台授权的插件类型（与 repository/plugins.KindTools、
// service/plugin.KindTools 同值；三层各自声明，避免为一个大写字符串引入依赖）。
const KindTools = "tools"

// Policy 一个「租户 × 插件」的授权策略。
//
// Blocked 单独给一个字段而不是把两种情况都压进 Allowed=false：调用方要说清
// 「平台已对本租户停用」还是「平台已全局停用」——前者可以找平台恢复，后者同样要
// 平台解闸，但影响面是全租户，两句话指向的处置范围不同。
type Policy struct {
	Allowed bool
	Blocked bool
}

// Default 没有配置行时的缺省策略：**开放**（也不拦）。
// 停用要显式写一行 is_allowed=false（见 Set），因为「没设过」与「设成关闭」
// 在管理端是两种不同的展示，不能靠缺省值猜。
func Default() Policy { return Policy{Allowed: true} }

// tableName 策略表全名（管理 schema 下）。
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".tenant_plugins"
}

// gateTableName 全局闸表全名（管理 schema 下）。
func gateTableName() string {
	return config.Cfg.Database.PlatformSchema + ".plugin_gates"
}

// Resolve 返回某租户对某插件生效的策略：(租户行 ?? 开放) AND NOT 全局闸。
//
// 合成一条 SQL（两个标量子查询）而不是打两趟库：本函数在每次插件调用前都会走，
// 多一次往返没有收益。
//
// 查询失败返回 error 而不是静默回退缺省：调用方必须自己决定放不放行，
// 而 Runner 的选择是 fail-closed —— 缺省虽已开放，但「读不出策略」不等于
// 「没人停用过它」，宁可错关成全员不可用，也不要在抖动时放行本该被停用的插件。
func Resolve(tenantID, pluginName string) (Policy, error) {
	var p Policy
	err := db.DB.QueryRow(`
		SELECT COALESCE((SELECT is_allowed FROM `+tableName()+`
		                  WHERE tenant_id = $1 AND plugin_name = $2 AND kind = $3), true),
		       COALESCE((SELECT blocked FROM `+gateTableName()+`
		                  WHERE plugin_name = $2 AND kind = $3), false)`,
		tenantID, pluginName, KindTools,
	).Scan(&p.Allowed, &p.Blocked)
	if err != nil {
		return Policy{}, err
	}
	if p.Blocked {
		p.Allowed = false // 硬闸：最后乘上去的 0，压过租户行
	}
	return p, nil
}

// Gates 被全局拦下的插件名集合（只含 blocked=true；查不到即不拦）。
//
// 返回 map 而不是全量列表：调用方只有一种用法 —— `if gates[name] { 关掉 }`，
// 缺省零值 false 正好是「不拦」。
func Gates() (map[string]bool, error) {
	rows, err := db.DB.Query(
		`SELECT plugin_name FROM `+gateTableName()+` WHERE kind = $1 AND blocked`, KindTools)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// GateBlocked 单个插件是否被全局闸拦下；没有行 = 不拦。
func GateBlocked(pluginName string) (bool, error) {
	var blocked bool
	err := db.DB.QueryRow(
		`SELECT blocked FROM `+gateTableName()+` WHERE plugin_name = $1 AND kind = $2`,
		pluginName, KindTools,
	).Scan(&blocked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return blocked, nil
}

// SetGate 设置（或覆盖）一个插件的闸。blocked=false 等价于删行，但会留一行记录。
func SetGate(pluginName string, blocked bool) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+gateTableName()+` (kind, plugin_name, blocked, updated_at)
		 VALUES ($1,$2,$3,CURRENT_TIMESTAMP)
		 ON CONFLICT (kind, plugin_name) DO UPDATE SET
		     blocked=EXCLUDED.blocked,
		     updated_at=CURRENT_TIMESTAMP`,
		KindTools, pluginName, blocked,
	)
	return err
}

// DeleteGate 删掉闸行 = 恢复缺省（不拦）。恢复应当走这里，而不是 SetGate(false)，
// 免得多一条「永远为 false」的记录让人以为闸被改过。
func DeleteGate(pluginName string) error {
	_, err := db.DB.Exec(
		`DELETE FROM `+gateTableName()+` WHERE plugin_name = $1 AND kind = $2`,
		pluginName, KindTools,
	)
	return err
}

// List 返回某租户**显式设有行**的策略（已乘上全局闸；未出现的插件按 Default()，即开放）。
// 管理端渲染插件列表时用它，避免逐个 Resolve 打出 N 条查询。
func List(tenantID string) (map[string]Policy, error) {
	rows, err := db.DB.Query(
		`SELECT plugin_name, is_allowed FROM `+tableName()+`
		 WHERE tenant_id = $1 AND kind = $2`, tenantID, KindTools)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Policy{}
	for rows.Next() {
		var name string
		var p Policy
		if err := rows.Scan(&name, &p.Allowed); err != nil {
			return nil, err
		}
		out[name] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 全局闸：与 capabilitypolicy.ResolveAll 同一套写法，最后乘上去的 0。
	gates, err := Gates()
	if err != nil {
		return nil, err
	}
	for name := range out {
		if gates[name] {
			p := out[name]
			p.Allowed = false
			p.Blocked = true
			out[name] = p
		}
	}
	return out, nil
}

// Rows 取某个 tenant_id 下的全部**原始行**（空串已不再表示系统行）。
// 平台编辑界面要区分「本租户设过」与「没设过」，所以不能只给生效值。
func Rows(tenantID string) (map[string]Policy, error) {
	rows, err := db.DB.Query(
		`SELECT plugin_name, is_allowed FROM `+tableName()+`
		 WHERE tenant_id = $1 AND kind = $2`, tenantID, KindTools)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Policy{}
	for rows.Next() {
		var name string
		var p Policy
		if err := rows.Scan(&name, &p.Allowed); err != nil {
			return nil, err
		}
		out[name] = p
	}
	return out, rows.Err()
}

// Get 取某租户的**原始行**（第二返回值 false = 没设过）。
// 编辑框必须绑它：拿生效值回填会把「未设置」静默压成具体值。
func Get(tenantID, pluginName string) (Policy, bool, error) {
	var p Policy
	err := db.DB.QueryRow(
		`SELECT is_allowed FROM `+tableName()+`
		 WHERE tenant_id = $1 AND plugin_name = $2 AND kind = $3`,
		tenantID, pluginName, KindTools,
	).Scan(&p.Allowed)
	if err == sql.ErrNoRows {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, err
	}
	return p, true, nil
}

// Set 写入（或覆盖）一行。tenantID 为空会被库里的 CHECK (tenant_id <> ”) 拒绝 ——
// 系统默认行已移除，这里不做兜底改写，让调用方的错误当场暴露。
func Set(tenantID, pluginName string, p Policy) error {
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (tenant_id, kind, plugin_name, is_allowed, updated_at)
		 VALUES ($1,$2,$3,$4,CURRENT_TIMESTAMP)
		 ON CONFLICT (tenant_id, kind, plugin_name) DO UPDATE SET
		     is_allowed=EXCLUDED.is_allowed,
		     updated_at=CURRENT_TIMESTAMP`,
		tenantID, KindTools, pluginName, p.Allowed,
	)
	return err
}

// Delete 删掉一行（删掉即回到缺省＝开放）。
func Delete(tenantID, pluginName string) error {
	_, err := db.DB.Exec(
		`DELETE FROM `+tableName()+` WHERE tenant_id = $1 AND plugin_name = $2 AND kind = $3`,
		tenantID, pluginName, KindTools,
	)
	return err
}
