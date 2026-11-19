package globalvars

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"flame/nexus/shared/auth"
	varsvc "flame/nexus/shared/globalvars"
	globalvarsrepo "flame/nexus/shared/repository/globalvars"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// 全局变量（{schema}.global_vars）
//
// 作用域是「本租户内全局」：所有智能体 / 工作流 / 技能都能引用，**不跨租户**。
// 与 flows.env_vars（环境变量，单流程）、flows.flow_vars（流程变量）同属
// <scope>_vars 一族；全局作用域的 scope 就是 global。
//
// 安全口径：
//   - kind=secret 的值在库里是密文（仓储层加解密，见 shared/secureval），
//     接口出参一律掩码（回 has_value，不回明文）；
//   - 导出资产包永远不带值 —— 包里只有引用名，结构上就带不出去。
// ============================================================================

var store = globalvarsrepo.NewRepository()

// nameRe 变量名字符集：与实体名同一套（字母数字下划线，不能数字开头）。
// 它会被写进节点输入的 source_key 与文档里的 ${global_vars.名字}，
// 带点 / 空格 / 中文都会让模板解析或文档替换失败。
var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// secretMask 掩码占位：值无论多长都回这一串，避免从长度反推内容
const secretMask = "••••••"

// toJSON 出参统一走这里：secret 不回明文，只告诉前端「有没有值」。
func toJSON(v *globalvarsrepo.GlobalVar) gin.H {
	if v == nil {
		return gin.H{}
	}
	out := gin.H{
		"name":        v.Name,
		"kind":        v.Kind,
		"description": v.Description,
		"updated_at":  v.UpdatedAt,
		"has_value":   v.Value != "",
	}
	if v.Kind == globalvarsrepo.KindSecret {
		// 有值给掩码，没值留空 —— 前端据此区分「已设置」与「还没设」
		if v.Value != "" {
			out["value"] = secretMask
		} else {
			out["value"] = ""
		}
	} else {
		out["value"] = v.Value
	}
	return out
}

// ListGlobalVars 列出本租户的全部全局变量（按 name 升序）。
func ListGlobalVars(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	list, err := store.List(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "读取全局变量失败: "+err.Error(), "")
		return
	}
	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, toJSON(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}

// saveRequest 新建 / 覆盖一个变量。
//
// value 用 *string：secret 类型下「不传或空串」表示**不修改原值**
// （界面上不回显明文，用户不改密码时提交的必然是空）。
type saveRequest struct {
	Name        string  `json:"name"`
	Value       *string `json:"value"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
}

// UpsertGlobalVar 新建或覆盖一个全局变量（按 name）。
func UpsertGlobalVar(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	var req saveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "请求格式错误: "+err.Error(), "")
		return
	}
	name := strings.TrimSpace(req.Name)
	if !nameRe.MatchString(name) {
		xhttp.ErrCode(c, "globalvar.invalid_name", nil, "变量名只能用字母、数字、下划线，不能以数字开头（≤64）", "")
		return
	}

	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = globalvarsrepo.KindText
	}
	switch kind {
	case globalvarsrepo.KindText, globalvarsrepo.KindSecret,
		globalvarsrepo.KindNumber, globalvarsrepo.KindBool:
	default:
		xhttp.ErrCode(c, "globalvar.invalid_type", nil, "类型只能是 text / secret / number / bool", "")
		return
	}

	prev, err := store.Get(info.Schema, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "读取全局变量失败: "+err.Error(), "")
		return
	}

	value := ""
	if req.Value != nil {
		value = *req.Value
	}
	// secret + 空值 + 已存在：保留原值（界面上不回显明文，空提交即「不改」）
	if kind == globalvarsrepo.KindSecret && value == "" && prev != nil && prev.Kind == globalvarsrepo.KindSecret {
		value = prev.Value
	}

	if err := validateValue(name, kind, value); err != nil {
		xhttp.ErrJSON(c, err.Error(), nil, "")
		return
	}

	row := &globalvarsrepo.GlobalVar{
		Name:        name,
		Value:       value,
		Kind:        kind,
		Description: strings.TrimSpace(req.Description),
		UpdatedBy:   xsql.UUID(info.ID),
	}
	if err := store.Upsert(info.Schema, row); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "保存全局变量失败: "+err.Error(), "")
		return
	}

	// 写后失效：本实例立刻生效；别的实例靠 5s TTL 自然过期
	varsvc.Invalidate(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{"variable": toJSON(row)}, "")
}

// DeleteGlobalVar 删除一个全局变量。
//
// 默认拦「仍被引用」：变量删掉后，引用它的节点与文档要到运行期才失败，
// 而那往往是很久以后的一次调用。删除这一刻是唯一能把「会坏掉什么」说清楚的机会，
// 所以先扫一遍引用面（ScanUsage），有引用就拒绝并列出位置。
//
// force=1 才真删：留着是因为「变量本身就建错了，引用是残留」这种情况确实存在，
// 但那必须是人在看过清单之后按下的第二下。
func DeleteGlobalVar(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrCode(c, "globalvar.name_required", nil, "变量名不能为空", "")
		return
	}

	// 扫不出来不等于没有引用：错误一律拒绝删除，不拿「未知」当「安全」
	if c.Query("force") != "1" {
		hits, uerr := ScanUsage(info.Schema, name)
		if uerr != nil {
			xhttp.ErrCode(c, "globalvar.in_use", nil, "无法确认引用面，已拒绝删除（确认要删请带 force=1）: "+uerr.Error(), "")
			return
		}
		if len(hits) > 0 {
			xhttp.ErrCode(c, "globalvar.referenced", gin.H{"name": name, "count": len(hits)},
				fmt.Sprintf("「%s」仍被 %d 处引用：%s。删除后这些地方运行时会直接失败；确认要删请带 force=1",
					name, len(hits), describeHits(hits)), "")
			return
		}
	}

	removed, err := store.Delete(info.Schema, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "删除全局变量失败: "+err.Error(), "")
		return
	}
	if !removed {
		xhttp.ErrCode(c, "globalvar.not_found", gin.H{"name": name}, "全局变量不存在: "+name, "")
		return
	}

	varsvc.Invalidate(info.Schema)
	xhttp.OkJSON(c, "Success", gin.H{"deleted": name}, "")
}

// validateValue 存之前把类型错误拦下：运行期才发现「声明 number 却存了 abc」，
// 排查成本远高于保存时一句报错。
func validateValue(name, kind, value string) error {
	switch kind {
	case globalvarsrepo.KindNumber:
		if strings.TrimSpace(value) == "" {
			return nil // 空 = 0，与 resolve 的取法一致
		}
		if !isNumber(strings.TrimSpace(value)) {
			return fmt.Errorf("变量 %s 声明为 number，值 %q 不是数字", name, value)
		}
	case globalvarsrepo.KindBool:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "true", "false", "0", "1", "yes", "no", "on", "off":
		default:
			return fmt.Errorf("变量 %s 声明为 bool，值 %q 不是布尔（true/false/1/0/yes/no/on/off）", name, value)
		}
	}
	return nil
}

func isNumber(s string) bool {
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// authInfo 取当前登录租户（管理端中间件已注入）。
func authInfo(c *gin.Context) (*auth.AuthInfo, bool) {
	value, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := value.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrJSON(c, "Invalid authentication information", nil, "")
		return nil, false
	}
	return info, true
}
