// Package capabilitypolicy 提供「租户 × 工具能力」的平台级策略（键名沿用 sandbox.*）。
//
// 与租户侧的分工（两层都要过，缺一不可）：
//
//	platform.tenant_capabilities  平台侧：这个租户允不允许用这类能力 —— 平台可写
//	{schema}.agent_tool_grants    租户侧：给哪个 agent 授权 —— 租户可写
//
// 为什么需要平台侧这一层：沙箱是**共享基础设施**（一台 sandboxd 服务所有租户）。
// 浏览器是常驻的 Chromium，shell 会留下常驻进程并可能出网 —— 成本与风险落在平台，
// 而开关原本只在租户手里。这与 pluginpolicy 把调用额度搬上平台是同一个理由：
// 租户能给自己放开的闸，等于没有闸。
//
// 语义是**两层**（缺省拒绝 × 全局闸）：
//
//		生效 = (租户行 ?? 未开通) AND NOT blocked
//
//	  - 租户行是唯一授权来源，没有行就是未开通（工具在目录里显示不可用，执行层直接拒）；
//	  - 全局闸（platform.capability_gates）是**急停**：一旦拦下，压过租户的显式开启。
//	    它不是「第三层回退」—— 回退链遇到有值的层就停，而硬闸必须能压过一切，
//	    所以是最后乘上去的 0，不是链上的一环。
//	  - 2026-10-21 移除了「系统默认行」（tenant_id = ''）：它只影响没有租户行的租户，
//	    和全局闸是两种东西，放在同一张表里会让管理员分不清「缺省值」和「急停」，
//	    也会误以为它是总闸。库里已用 CHECK (tenant_id <> '') 封死，不再允许出现。
//	  - 不做缓存：一次工具解析多两条主键查询，换「平台改完立刻生效」的口径正确性。
//	    急停场景下这一点是硬要求。
//
// ⚠️ 所有入口都先过 capability.Normalize：2026-10 把 sandbox.script 更名为
// sandbox.code，库里可能还有旧行。归一必须同时覆盖读（Resolve / ResolveAll / Gates）
// 与写（Set / SetGate），否则会出现「读旧键、写新键」的双行同义记录。
//
// 存量提示：本表上线前没有平台行，迁移脚本按「已经真的在用」回填（见
// nexus/platform/sql/tenant_capabilities.sql），否则存量租户的 exec/browser 会立刻失效。
package capabilitypolicy

import (
	"database/sql"
	"strings"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/tools/xlog"
)

// Policy 一个「租户 × 能力」的策略。目前只有「允不允许」；
// 并发/额度若要加，按 pluginpolicy 的形状扩列即可（browser 会话数是典型候选）。
type Policy struct {
	Allowed bool
}

// Default 没有配置行时的缺省策略：**未开通**。
func Default() Policy { return Policy{Allowed: false} }

// tableName 策略表全名（管理 schema 下）。
func tableName() string {
	return config.Cfg.Database.PlatformSchema + ".tenant_capabilities"
}

// Resolve 返回某租户对某能力的生效策略：(租户行 ?? 未开通) AND NOT 全局闸。
//
// 查询失败返回 error 而不是静默回退缺省：调用方必须自己决定放不放行，
// 运行期闸门的选择是 fail-closed（一次 DB 抖动不该等于全员开通）。
func Resolve(tenantID, key string) (Policy, error) {
	key = capability.Normalize(key)
	p := Default()
	err := db.DB.QueryRow(
		`SELECT is_allowed FROM `+tableName()+` WHERE tenant_id = $1 AND capability = $2`,
		tenantID, key,
	).Scan(&p.Allowed)
	if err != nil && err != sql.ErrNoRows {
		return Policy{}, err
	}

	blocked, err := GateBlocked(key)
	if err != nil {
		return Policy{}, err
	}
	if blocked {
		p.Allowed = false
	}
	return p, nil
}

// ResolveAll 返回某租户全部**已知能力**的生效结论（缺省拒绝：没配的一律 false）。
//
// 形状是 map[string]bool 而不是 map[string]Policy：调用方只有两种用途 ——
// 目录算可用性、运行期判闸门，两者都只关心「开没开」。
func ResolveAll(tenantID string) (map[string]bool, error) {
	rows, err := db.DB.Query(
		`SELECT capability, is_allowed FROM `+tableName()+` WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool, len(capability.All()))
	for _, key := range capability.All() {
		out[key] = false // 缺省拒绝：没有租户行就是未开通
	}
	for rows.Next() {
		var key string
		var allowed bool
		if err := rows.Scan(&key, &allowed); err != nil {
			return nil, err
		}
		// 旧键归一：不归一的话 sandbox.script 的行会落在 All() 之外，
		// Snapshot 过滤旧键后表现为「本来开着却显示未开通」。
		out[capability.Normalize(key)] = allowed
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 全局闸：最后乘上去的 0，压过上面所有结果（急停必须能盖住租户的显式开启）。
	gates, err := Gates()
	if err != nil {
		return nil, err
	}
	for key := range out {
		if gates[key] {
			out[key] = false
		}
	}
	return out, nil
}

// Snapshot 取某租户的能力快照（键齐全，缺省 false）。
//
// 读失败按**全部未开通**返回（fail-closed）并记日志：能力是成本护栏，
// 一次 DB 抖动放宽成「全员可执行」比拒绝一次调用危险得多。
// 目录展示与运行期闸门都走它 —— 两边必须同向，否则界面说可用、执行时被拒。
func Snapshot(schema string) map[string]bool {
	all, err := ResolveAll(schema)
	if err != nil {
		hint := ""
		if strings.Contains(err.Error(), "does not exist") {
			// 上线顺序错了（表还没建）时给出可操作的线索，否则只看到「执行被拒」会查很久
			hint = " —— 请先执行 nexus/platform/sql/tenant_capabilities.sql"
		}
		xlog.Error("读取租户能力失败 schema=%s（按「全部未开通」处理）: %v%s", schema, err, hint)
		all = map[string]bool{}
	}
	out := make(map[string]bool, len(capability.All()))
	for _, key := range capability.All() {
		out[key] = all[key]
	}
	return out
}

// ---- 全局硬闸（platform.capability_gates）----
//
// 语义与租户行**相反**：缺省放行。没有行 = 不拦，blocked=true 才拦。
// 这个缺省是刻意反过来的 —— 对闸来说「忘配置」应当等于「不拦」，
// 否则加一个闸就等于把所有人的能力悄悄关掉。
//
// 它只有两态（拦 / 不拦），没有「未设置」这个独立语义：设成 false 与删行等价，
// 不存在「系统默认」那套三态困惑。

// gateTableName 全局闸表全名。
func gateTableName() string {
	return config.Cfg.Database.PlatformSchema + ".capability_gates"
}

// Gates 被拦下的能力集合（只含 blocked=true 的键；查不到即不拦）。
//
// 返回 map 而不是全量列表：调用方只有一种用法 —— `if gates[key] { 关掉 }`，
// 缺省零值 false 正好是「不拦」。
func Gates() (map[string]bool, error) {
	rows, err := db.DB.Query(`SELECT capability FROM ` + gateTableName() + ` WHERE blocked`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		// 旧键归一：急停行若还是 sandbox.script，不归一就等于急停失效。
		out[capability.Normalize(key)] = true
	}
	return out, rows.Err()
}

// GateBlocked 单项能力是否被全局闸拦下；没有行 = 不拦。
func GateBlocked(key string) (bool, error) {
	key = capability.Normalize(key)
	var blocked bool
	err := db.DB.QueryRow(
		`SELECT blocked FROM `+gateTableName()+` WHERE capability = $1`, key,
	).Scan(&blocked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return blocked, nil
}

// SetGate 设置（或覆盖）一项能力的闸。blocked=false 等价于删行，但会留一行记录。
func SetGate(key string, blocked bool) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(
		`INSERT INTO `+gateTableName()+` (capability, blocked, updated_at)
		 VALUES ($1,$2,CURRENT_TIMESTAMP)
		 ON CONFLICT (capability) DO UPDATE SET
		     blocked=EXCLUDED.blocked,
		     updated_at=CURRENT_TIMESTAMP`,
		key, blocked,
	)
	return err
}

// DeleteGate 删掉闸行 = 恢复缺省（不拦）。恢复应当走这里，而不是 SetGate(false)，
// 免得多一条「永远为 false」的记录让人以为闸被改过。
func DeleteGate(key string) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(`DELETE FROM `+gateTableName()+` WHERE capability = $1`, key)
	return err
}

// Rows 取某个 tenant_id 下的全部**原始行**（空串已不再表示系统行）。
// 平台编辑界面要区分「本租户设过」与「没设过」，所以不能只给生效值。
func Rows(tenantID string) (map[string]Policy, error) {
	rows, err := db.DB.Query(
		`SELECT capability, is_allowed FROM `+tableName()+` WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Policy{}
	for rows.Next() {
		var key string
		var p Policy
		if err := rows.Scan(&key, &p.Allowed); err != nil {
			return nil, err
		}
		out[capability.Normalize(key)] = p
	}
	return out, rows.Err()
}

// Get 取某租户的**原始行**（第二返回值 false = 没设过）。
// 编辑框必须绑它：拿生效值回填会把「未设置」静默压成具体值。
func Get(tenantID, key string) (Policy, bool, error) {
	key = capability.Normalize(key)
	var p Policy
	err := db.DB.QueryRow(
		`SELECT is_allowed FROM `+tableName()+` WHERE tenant_id = $1 AND capability = $2`,
		tenantID, key,
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
func Set(tenantID, key string, p Policy) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(
		`INSERT INTO `+tableName()+` (tenant_id, capability, is_allowed, updated_at)
		 VALUES ($1,$2,$3,CURRENT_TIMESTAMP)
		 ON CONFLICT (tenant_id, capability) DO UPDATE SET
		     is_allowed=EXCLUDED.is_allowed,
		     updated_at=CURRENT_TIMESTAMP`,
		tenantID, key, p.Allowed,
	)
	return err
}

// Delete 删掉一行（删掉即回到缺省＝未开通）。
func Delete(tenantID, key string) error {
	key = capability.Normalize(key)
	_, err := db.DB.Exec(
		`DELETE FROM `+tableName()+` WHERE tenant_id = $1 AND capability = $2`,
		tenantID, key,
	)
	return err
}
