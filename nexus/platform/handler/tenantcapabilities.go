package handler

import (
	"strings"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// 工具能力的平台级授权：platform.tenant_capabilities + platform.capability_gates 的读写。
//
// 键名沿用 sandbox.exec / sandbox.browser（不改库、不改接口），但语义是「这类工具
// 能不能用」：界面统一叫「能力」，与插件授权摆在同一页（形状完全一致）。
//
// 两层，缺一不可：
//
//	租户行   这个租户允不允许（缺省拒绝：没有行 = 未开通）—— 编辑框绑原始行
//	全局闸   这项能力还开不开（缺省放行：没有行 = 不拦）—— 急停，压过租户的显式开启
//
// 生效 = (租户行 ?? 未开通) AND NOT blocked。2026-10-21 移除了「系统默认行」
// （tenant_id = ''）：它只改缺省值、与急停是两种东西，混在一张表里会让管理员
// 分不清「我关的」和「本来就没有」，也会误以为它是总闸。
//
// 表结构见 nexus/platform/sql/tenant_capabilities.sql 与 capability_gates.sql；
// 解析逻辑见 shared/capabilitypolicy。
// 为什么不做租户侧开关：能力没有「用哪个实现/key」这类租户配置面，二级开关没有约束力。

// capabilityView 一行能力：生效值 + 本租户原始值 + 全局闸状态。
type capabilityView struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Allowed bool   `json:"allowed"` // 生效值（已乘上全局闸）
	Row     *bool  `json:"row"`     // 本租户原始行；null = 没设过（未开通）
	Blocked bool   `json:"blocked"` // 全局闸是否拦下（false = 不拦，含「没有闸行」）
}

// buildCapabilityViews 把「本租户行 + 全局闸」合并成列表（顺序按 capability.All）。
//
// Row 用 *bool 而不是 bool：null（没设过 = 未开通）与 false（显式关闭）是两种意思，
// 压成一个布尔会让编辑框把「未设置」静默写成「关闭」。
func buildCapabilityViews(rows map[string]capabilitypolicy.Policy, gates map[string]bool) []capabilityView {
	out := make([]capabilityView, 0, len(capability.All()))
	for _, key := range capability.All() {
		v := capabilityView{Key: key, Label: capability.Label(key)}

		eff := capabilitypolicy.Default() // 未开通
		if p, ok := rows[key]; ok {
			eff = p
			b := p.Allowed
			v.Row = &b
		}
		if gates[key] {
			v.Blocked = true
			eff.Allowed = false // 硬闸最后乘上去的 0，排除在「原始行」之外
		}
		v.Allowed = eff.Allowed
		out = append(out, v)
	}
	return out
}

// ListTenantCapabilities GET /platform/tenants/:schema/capabilities
func ListTenantCapabilities(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	rows, err := capabilitypolicy.Rows(schema)
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load tenant capabilities: "+err.Error(), nil, "")
		return
	}
	gates, err := capabilitypolicy.Gates()
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load capability gates: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"capabilities": buildCapabilityViews(rows, gates)}, "")
}

// ListCapabilityGates GET /platform/system/capability-gates
//
// 键齐全（顺序按 capability.All），未设闸的一律 blocked=false —— 界面是一排开关，
// 不需要区分「没设过」和「设成不拦」：两者行为完全一致。
func ListCapabilityGates(c *gin.Context) {
	gates, err := capabilitypolicy.Gates()
	if err != nil {
		xhttp.ErrJSON(c, "Failed to load capability gates: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"capabilities": buildCapabilityViews(nil, gates)}, "")
}

// capabilityDeps 能力的依赖键（**任一**开通即可；空 = 无依赖）。
//
// net.egress 依赖 exec 或 shell：它约束的是「命令执行族能否出网」，而这两个通道
// 都能执行任意代码 —— 只开 shell 的租户同样需要出网能力，只写 exec 会把它挡在门外。
//
// 依赖只在**写入时**挡，运行时不做级联：exec/shell 都关掉后 egress 变孤儿是无害的 ——
// 它本来就只在执行时才生效，不会凭空多出一个可出网的通道。
func capabilityDeps(key string) []string {
	if key == capability.Egress {
		return []string{capability.Exec, capability.Shell}
	}
	return nil
}

// capabilityEnabled 某租户下某能力的生效值（缺省未开通）。
//
// 读失败直接上抛：这是写库前的准入判断，拿不准就拒绝，不要在最宽松的方向上兜底。
func capabilityEnabled(tenantID, key string) (bool, error) {
	rows, err := capabilitypolicy.Rows(tenantID)
	if err != nil {
		return false, err
	}
	p, ok := rows[key]
	return ok && p.Allowed, nil // 没有行 = 未开通（缺省拒绝）
}

// setCapabilityReq 写入一行；allowed 省略视为 false（缺省拒绝，不做「保留原值」的猜测）。
type setCapabilityReq struct {
	Allowed *bool `json:"allowed"`
}

// SetTenantCapabilityPolicy PUT /platform/tenants/:schema/capabilities/:key
func SetTenantCapabilityPolicy(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	key := c.Param("key")
	if !capability.Valid(key) {
		xhttp.ErrJSON(c, "unknown capability: "+key, nil, "")
		return
	}
	var body setCapabilityReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	allowed := body.Allowed != nil && *body.Allowed
	// 依赖准入：放开依赖项之前，被依赖的能力至少有一个已经生效（否则配出来的是空组合）。
	if deps := capabilityDeps(key); allowed && len(deps) > 0 {
		var ok bool
		for _, dep := range deps {
			v, err := capabilityEnabled(schema, dep)
			if err != nil {
				xhttp.ErrJSON(c, "Failed to check "+dep+": "+err.Error(), nil, "")
				return
			}
			if v {
				ok = true
				break
			}
		}
		if !ok {
			xhttp.ErrCode(c, "capability.dependency_missing", gin.H{"capability": key, "deps": strings.Join(deps, " / ")},
				key+" 依赖 "+strings.Join(deps, " 或 ")+
					"：请先开通其中之一，否则只是「允许出网但不能执行」的空组合", "")
			return
		}
	}
	if err := capabilitypolicy.Set(schema, key, capabilitypolicy.Policy{Allowed: allowed}); err != nil {
		xhttp.ErrJSON(c, "Failed to save capability: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"key": key, "allowed": allowed}, "")
}

// DeleteTenantCapabilityPolicy DELETE /platform/tenants/:schema/capabilities/:key
func DeleteTenantCapabilityPolicy(c *gin.Context) {
	schema, ok := mustTenant(c)
	if !ok {
		return
	}
	key := c.Param("key")
	if !capability.Valid(key) {
		xhttp.ErrJSON(c, "unknown capability: "+key, nil, "")
		return
	}
	if err := capabilitypolicy.Delete(schema, key); err != nil {
		xhttp.ErrJSON(c, "Failed to delete capability: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"key": key}, "")
}

// setGateReq 全局闸写入；blocked 省略视为 false（= 不拦）。
type setGateReq struct {
	Blocked *bool `json:"blocked"`
}

// SetCapabilityGate PUT /platform/system/capability-gates/:key
//
// blocked=false 走删行而不是 SetGate(false)：闸表只保留「确实被拦」的记录，
// 否则排查时看到一行 false 会以为「有人动过这个闸」。
func SetCapabilityGate(c *gin.Context) {
	key := c.Param("key")
	if !capability.Valid(key) {
		xhttp.ErrJSON(c, "unknown capability: "+key, nil, "")
		return
	}
	var body setGateReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	blocked := body.Blocked != nil && *body.Blocked
	var err error
	if blocked {
		err = capabilitypolicy.SetGate(key, true)
	} else {
		err = capabilitypolicy.DeleteGate(key)
	}
	if err != nil {
		xhttp.ErrJSON(c, "Failed to save capability gate: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"key": key, "blocked": blocked}, "")
}

// DeleteCapabilityGate DELETE /platform/system/capability-gates/:key
func DeleteCapabilityGate(c *gin.Context) {
	key := c.Param("key")
	if !capability.Valid(key) {
		xhttp.ErrJSON(c, "unknown capability: "+key, nil, "")
		return
	}
	if err := capabilitypolicy.DeleteGate(key); err != nil {
		xhttp.ErrJSON(c, "Failed to delete capability gate: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"key": key}, "")
}
