package handler

import (
	"strings"

	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// Platform-level grants for tool capabilities: reading and writing platform.tenant_capabilities plus
// platform.capability_gates.
//
// The key names stay sandbox.exec / sandbox.browser (no database or API change), but the meaning is "may this kind of tool be
// used": the UI calls them "capabilities" throughout and puts them on the same page as plugin grants (the shape is identical).
//
// Two layers, both required:
//
//	tenant row  whether this tenant may (deny by default: no row = not enabled) - the edit form binds to the raw row
//	global gate whether the capability is on at all (allow by default: no row = not blocked) - the emergency stop, overriding
//	            a tenant's explicit enable
//
// Effective = (tenant row ?? not enabled) AND NOT blocked. On 2026-10-21 the "system default row" (tenant_id = '') was removed:
// it only changed the default and is a different thing from the emergency stop, and keeping both in one table left
// administrators unable to tell "I turned it off" from "it was never there", or even mistook it for a master switch.
//
// The DDL lives in nexus/platform/sql/tenant_capabilities.sql and capability_gates.sql; the resolution logic in
// shared/capabilitypolicy.
// Why there is no tenant-side switch: a capability has no tenant configuration surface such as "which implementation / key",
// so a second-level switch would have no force.

// capabilityView is one capability row: effective value + this tenant's raw value + global gate state.
type capabilityView struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Allowed bool   `json:"allowed"` // effective value (already multiplied by the global gate)
	Row     *bool  `json:"row"`     // this tenant's raw row; null = never set (not enabled)
	Blocked bool   `json:"blocked"` // whether the global gate stops it (false = no, including "no gate row")
}

// buildCapabilityViews merges "this tenant's rows + the global gate" into a list (ordered by capability.All).
//
// Row is a *bool rather than a bool: null (never set = not enabled) and false (explicitly off) mean different things, and
// collapsing them into one boolean would make the edit form silently turn "unset" into "off".
func buildCapabilityViews(rows map[string]capabilitypolicy.Policy, gates map[string]bool) []capabilityView {
	out := make([]capabilityView, 0, len(capability.All()))
	for _, key := range capability.All() {
		v := capabilityView{Key: key, Label: capability.Label(key)}

		eff := capabilitypolicy.Default() // not enabled
		if p, ok := rows[key]; ok {
			eff = p
			b := p.Allowed
			v.Row = &b
		}
		if gates[key] {
			v.Blocked = true
			eff.Allowed = false // the hard gate's 0 multiplied in last, kept out of the "raw row"
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
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load tenant capabilities: "+err.Error(), "")
		return
	}
	gates, err := capabilitypolicy.Gates()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load capability gates: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"capabilities": buildCapabilityViews(rows, gates)}, "")
}

// ListCapabilityGates GET /platform/system/capability-gates
//
// Every key is present (ordered by capability.All), and an ungated one is blocked=false - the UI is a row of switches and
// does not need to tell "never set" from "set to not blocked": both behave identically.
func ListCapabilityGates(c *gin.Context) {
	gates, err := capabilitypolicy.Gates()
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to load capability gates: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"capabilities": buildCapabilityViews(nil, gates)}, "")
}

// capabilityDeps lists a capability's dependency keys (**any one** enabled is enough; empty = no dependency).
//
// net.egress depends on exec or shell: what it constrains is "can the command-execution family reach the network", and both
// channels can run arbitrary code - a tenant that only enabled shell needs egress just as much, and listing exec alone would
// lock it out.
//
// Dependencies are only enforced **on write**, never cascaded at runtime: egress becoming an orphan once both exec and shell
// are off is harmless - it only takes effect during execution anyway and cannot conjure a new network channel.
func capabilityDeps(key string) []string {
	if key == capability.Egress {
		return []string{capability.Exec, capability.Shell}
	}
	return nil
}

// capabilityEnabled is a capability's effective value for one tenant (not enabled by default).
//
// A read failure is propagated: this is an admission check before a write, and when in doubt it refuses rather than falling
// back in the most permissive direction.
func capabilityEnabled(tenantID, key string) (bool, error) {
	rows, err := capabilitypolicy.Rows(tenantID)
	if err != nil {
		return false, err
	}
	p, ok := rows[key]
	return ok && p.Allowed, nil // no row = not enabled (deny by default)
}

// setCapabilityReq writes one row; an omitted allowed counts as false (deny by default, no guessing "keep the old value").
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
		xhttp.ErrCode(c, "common.internal", nil, "unknown capability: "+key, "")
		return
	}
	var body setCapabilityReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	allowed := body.Allowed != nil && *body.Allowed
	// Dependency admission: before enabling a dependent capability, at least one of its dependencies must already be
	// effective (otherwise the result is an empty combination).
	if deps := capabilityDeps(key); allowed && len(deps) > 0 {
		var ok bool
		for _, dep := range deps {
			v, err := capabilityEnabled(schema, dep)
			if err != nil {
				xhttp.ErrCode(c, "common.internal", nil, "Failed to check "+dep+": "+err.Error(), "")
				return
			}
			if v {
				ok = true
				break
			}
		}
		if !ok {
			xhttp.ErrCode(c, "capability.dependency_missing", gin.H{"capability": key, "deps": strings.Join(deps, " / ")},
				key+" depends on "+strings.Join(deps, " or ")+
					": enable one of them first, otherwise this is an empty combination of \"network allowed but nothing to run\"", "")
			return
		}
	}
	if err := capabilitypolicy.Set(schema, key, capabilitypolicy.Policy{Allowed: allowed}); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to save capability: "+err.Error(), "")
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
		xhttp.ErrCode(c, "common.internal", nil, "unknown capability: "+key, "")
		return
	}
	if err := capabilitypolicy.Delete(schema, key); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to delete capability: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"key": key}, "")
}

// setGateReq writes the global gate; an omitted blocked counts as false (= do not block).
type setGateReq struct {
	Blocked *bool `json:"blocked"`
}

// SetCapabilityGate PUT /platform/system/capability-gates/:key
//
// blocked=false deletes the row instead of SetGate(false): the gate table keeps only "actually blocked" records, otherwise
// someone troubleshooting would see a false row and assume "somebody touched this gate".
func SetCapabilityGate(c *gin.Context) {
	key := c.Param("key")
	if !capability.Valid(key) {
		xhttp.ErrCode(c, "common.internal", nil, "unknown capability: "+key, "")
		return
	}
	var body setGateReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
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
		xhttp.ErrCode(c, "common.internal", nil, "Failed to save capability gate: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"key": key, "blocked": blocked}, "")
}

// DeleteCapabilityGate DELETE /platform/system/capability-gates/:key
func DeleteCapabilityGate(c *gin.Context) {
	key := c.Param("key")
	if !capability.Valid(key) {
		xhttp.ErrCode(c, "common.internal", nil, "unknown capability: "+key, "")
		return
	}
	if err := capabilitypolicy.DeleteGate(key); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "Failed to delete capability gate: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"key": key}, "")
}
