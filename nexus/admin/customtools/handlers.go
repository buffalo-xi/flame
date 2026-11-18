// Package customtools 租户自定义自定义工具的管理端接口。
//
// 与插件的差别（决定了这里为什么是 CRUD 而不是"配置"）：
//   - 插件本体在磁盘上被发现，管理端只能改"启用哪个实现、配什么 key"；
//   - 自定义工具是**租户自己创建**的实体：定义、参数 schema、代码都在这一侧。
//
// 三个刻意的取舍：
//  1. name 创建后不可改 —— 它已经写进 agent_tool_grants.ref（custom:<name>），
//     改名等于让别人的授权静默失效；
//  2. secret 字段回显打码、带回打码串表示"不改"（与 admin/plugins 同一套约定）；
//  3. 删除时一并删盘上的代码目录 —— 留一个孤儿目录只会让下次创建困惑。
package customtools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/sandboxinfo"
	"flame/nexus/shared/toolfile"
	"flame/nexus/shared/versioning"
	"flame/tools/xhttp"
	"flame/tools/xlog"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// secretMask 与 admin/plugins 同款：回显打码、带回打码串 = 没改。
const secretMask = "••••"

// storeFor 每次请求按 schema 现构合并视图（目录为准 + 表提供 status/configs）。
// 不做全局单例：schema 是请求态，缓一个实例只会读到别的租户。
func storeFor(schema string) *customtoolstore.Store {
	return customtoolstore.New(config.Cfg.Agent.RootDir, schema)
}

func mustAuth(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrJSON(c, "Authentication information not found", nil, "")
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrJSON(c, "Invalid authentication information", nil, "")
		return nil, false
	}
	return info, true
}

// customToolView 列表项 = 行 + 打码后的配置值 + 契约来源 + 当前版本号。
type customToolView struct {
	models.CustomTool
	Configs map[string]any `json:"configs"` // 覆盖内嵌字段：secret 已打码
	// Contract 这份契约是从 TOOL.json 来的还是回退到表了。
	// 前端据此提示「未文件化 / 文件有错」，避免面板与执行侧看到两套定义却没人知道。
	Contract toolfile.Contract `json:"contract"`
	// CurrentVersion 当前发布版本（0 = 尚未发布）。卡片上直接显示 vN / 未发布。
	CurrentVersion int `json:"current_version"`
}

func buildView(t *models.CustomTool, c toolfile.Contract) customToolView {
	return customToolView{CustomTool: *t, Configs: maskConfigs(t), Contract: c}
}

// maskConfigs 按 config_fields 的 secret 声明打码。
func maskConfigs(t *models.CustomTool) map[string]any {
	cfg := t.ConfigMap()
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range t.ConfigFieldList() {
		if !f.Secret {
			continue
		}
		if s, ok := out[f.Key].(string); ok && s != "" {
			out[f.Key] = maskValue(s)
		}
	}
	return out
}

func maskValue(s string) string {
	if len(s) <= 4 {
		return secretMask
	}
	return secretMask + s[len(s)-4:]
}

// mergeConfigs 已存的配置打底，再用请求值覆盖（打码串跳过）。
func mergeConfigs(stored map[string]any, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok && strings.HasPrefix(s, secretMask) {
			continue // 打码回传 = 没改
		}
		out[k] = v
	}
	return out
}

// ListCustomTools GET /customtools/
func ListCustomTools(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	list, issues, err := storeFor(info.Schema).ListWithIssues()
	if err != nil {
		xhttp.ErrJSON(c, "读取自定义工具失败: "+err.Error(), nil, "")
		return
	}
	// 打开列表即完成存量迁移：有表行、没 TOOL.json 的补写一份（幂等；失败只记日志，
	// 工具仍按表定义工作）。放在管理端而不是读路径：读一次就把盘改了，副作用太隐蔽。
	if written, errs := toolfile.EnsureAll(config.Cfg.Agent.RootDir, info.Schema, list); written > 0 || len(errs) > 0 {
		xlog.Info("自定义工具契约文件补齐 schema=%s written=%d errors=%d", info.Schema, written, len(errs))
		for _, e := range errs {
			xlog.Error("补齐 TOOL.json 失败 schema=%s: %s", info.Schema, e)
		}
	}

	// 当前版本号：一次批量查（卡片要显示 vN），读失败不影响列表本身
	curVers, _ := versioning.CurrentVersions(info.Schema, "tool_versions")

	out := make([]customToolView, 0, len(list))
	for i := range list {
		c := toolfile.Load(config.Cfg.Agent.RootDir, info.Schema, &list[i])
		v := buildView(&list[i], c)
		v.CurrentVersion = curVers[list[i].ID]
		out = append(out, v)
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"tools": out,
		// 目录层的问题（缺 TOOL.json / 契约写坏 / 记录残留）。
		// 「盘上有目录、平台看不见」那个坑，现在至少能以告警形式被看见。
		"issues": issues,
		// 沙箱事实：当前部署能跑哪些语言。前端据此标注语言下拉，
		// 而不是让用户选了 lua 才发现镜像里没装（错误会拖到第一次调用才炸）。
		// 读的是 sandboxd 能力快照（进程内缓存），不查库、不发额外请求。
		"sandbox": sandboxView(),
	}, "")
}

// sandboxView 沙箱语言可用性（给管理端展示用）。
//
// ready=false 表示还没成功拉到过 capabilities：此时不能猜环境，
// 前端应如实显示「可用性未知」，而不是把四个语言都当成可用。
func sandboxView() gin.H {
	info := sandboxinfo.Get()
	langs := info.Langs
	if langs == nil {
		langs = []string{}
	}
	return gin.H{
		"ready": sandboxinfo.Ready(),
		"mode":  info.Mode,
		"langs": langs,
	}
}

// errToolNotFound 自定义工具不存在（编码 tool.not_found）。
//
// 名字统一从路由参数取，不要求调用方各自拼：这些 handler 的路由都带 :name，
// 个别分支只剩实体行（如按 id 锁行后才发现没了）也在同一路由下，取得到。
// msg 仍是中文原文，前端行为不变。
func errToolNotFound(c *gin.Context) {
	xhttp.ErrCode(c, "tool.not_found", map[string]any{"name": c.Param("name")}, "自定义工具不存在", "")
}

// GetCustomTool GET /customtools/:name
func GetCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	t, err := storeFor(info.Schema).Get(info.Schema, c.Param("name"))
	if err != nil {
		errToolNotFound(c)
		return
	}
	// 先合并契约再读入口文件：entry 可能只在 TOOL.json 里改过（表里还是旧的）。
	contract := toolfile.Load(config.Cfg.Agent.RootDir, info.Schema, t)
	body := gin.H{"tool": buildView(t, contract)}
	if content, cerr := readEntry(info.Schema, t); cerr == nil {
		body["script"] = content
	}
	// 版本状态：编辑器头部要能区分「未发布 / 已发布但有改动 / 与线上一致」（与 flow 同口径）。
	body["current_version"], body["dirty"] = 0, false
	if st := toolVerStore(); st != nil {
		v, dirty := versioning.ToolVersionState(context.Background(), st, info.Schema, t.Name)
		body["current_version"], body["dirty"] = v, dirty
	}
	xhttp.OkJSON(c, "Success", body, "")
}

// saveReq 创建/更新的请求体。字段与表一一对应，避免"表单里有、库里没有"。
//
// 字符串字段用指针区分两种"空"：nil = 不改（更新时），"" = 清空。
// 不用值类型是因为「把描述删干净」是合法操作，值类型只能表达"没传"，
// 会让用户看到「保存成功但值没变」。与已有的 Status / Egress / Script 同口径。
type saveReq struct {
	Name           string           `json:"name"` // 仅创建时使用；更新时忽略（改名会打断授权 ref）
	DisplayName    *string          `json:"display_name"`
	Description    *string          `json:"description"`
	Parameters     map[string]any   `json:"parameters"`
	Output         map[string]any   `json:"output"`
	Lang           *string          `json:"lang"`
	Entry          *string          `json:"entry"`
	TimeoutMS      int              `json:"timeout_ms"`
	MaxOutputBytes int              `json:"max_output_bytes"`
	Egress         *bool            `json:"egress"`
	ConfigFields   []map[string]any `json:"config_fields"`
	Configs        map[string]any   `json:"configs"`
	Status         *int             `json:"status"`
	Script         *string          `json:"script"`
}

// derefStr 指针取值；nil 当空串（创建路径用，让"没传"与"传空"落到同一个默认值）。
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// CreateCustomTool POST /customtools/create
func CreateCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	var body saveReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	name := strings.TrimSpace(body.Name)
	if !tools.ValidName(name) {
		// 与 tools.ValidName（paths.ValidName）的实际规则一致：不含下划线。
		// 早先的文案写成「下划线与短横线」，与校验器对不上，会让人白试。
		xhttp.ErrJSON(c, "工具名只能包含字母、数字与短横线，且不能以短横线开头", nil, "")
		return
	}
	if strings.Contains(name, "__") {
		xhttp.ErrJSON(c, "工具名不能包含连续下划线（对外名是 custom__<name>，会把前缀切乱）", nil, "")
		return
	}
	if _, err := storeFor(info.Schema).Get(info.Schema, name); err == nil {
		xhttp.ErrJSON(c, "同名自定义工具已存在", nil, "")
		return
	}

	// 同名重建：清掉上一个同名实体留下的版本目录。DB 里没有行不代表盘上没有，
	// 留着它会让新工具「继承」旧历史（发布取号、打包都会对不上）。
	PurgeToolVersions(info.Schema, name)

	t := &models.CustomTool{
		Name:           name,
		DisplayName:    derefStr(body.DisplayName),
		Description:    strings.TrimSpace(derefStr(body.Description)),
		Parameters:     xsql.JSONB{Data: body.Parameters},
		Output:         xsql.JSONB{Data: body.Output},
		Lang:           defaultLang(derefStr(body.Lang)),
		Entry:          defaultEntry(derefStr(body.Entry), derefStr(body.Lang)),
		TimeoutMS:      body.TimeoutMS,
		MaxOutputBytes: body.MaxOutputBytes,
		Egress:         body.Egress != nil && *body.Egress,
		ConfigFields:   xsql.JSONB{Data: body.ConfigFields},
		Configs:        xsql.JSONB{Data: body.Configs},
		Status:         models.StatusNormal,
		CreatedBy:      info.Name,
	}
	if body.Status != nil {
		t.Status = *body.Status
	}
	if err := storeFor(info.Schema).Create(info.Schema, t); err != nil {
		xhttp.ErrJSON(c, "创建自定义工具失败: "+err.Error(), nil, "")
		return
	}

	// 先落库再落盘：目录建失败时回滚那一行，避免"库里有、盘上没有"的半成品。
	if err := toolfile.WriteOf(config.Cfg.Agent.RootDir, info.Schema, t); err != nil {
		_ = storeFor(info.Schema).Delete(info.Schema, name)
		xhttp.ErrJSON(c, "写入契约文件失败: "+err.Error(), nil, "")
		return
	}
	if err := writeEntry(info.Schema, t, entryContent(body.Script, t)); err != nil {
		_ = storeFor(info.Schema).Delete(info.Schema, name)
		_ = os.RemoveAll(toolDir(info.Schema, name))
		xhttp.ErrJSON(c, "写入实现文件失败: "+err.Error(), nil, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"tool": buildView(t, toolfile.Contract{Source: "file"})}, "")
}

// UpdateCustomTool PUT /customtools/:name
func UpdateCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	cur, err := storeFor(info.Schema).Get(info.Schema, name)
	if err != nil {
		errToolNotFound(c)
		return
	}
	// 以**文件里的契约**为基线再叠加本次改动：读路径是文件优先，
	// 若从表行起改，一次保存就会把文件里手改过的契约悄悄盖回旧值。
	toolfile.Load(config.Cfg.Agent.RootDir, info.Schema, cur)
	var body saveReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}

	// 改名不允许：ref 已经写进授权表，改名会让别人的授权静默失效。
	if body.Lang != nil && strings.TrimSpace(*body.Lang) != "" {
		cur.Lang = defaultLang(*body.Lang)
	}
	// 入口文件不允许清空：空串回落该语言的默认入口，否则工具会指向一个不存在的文件。
	if body.Entry != nil {
		if e := strings.TrimSpace(*body.Entry); e != "" {
			cur.Entry = e
		} else {
			cur.Entry = defaultEntry("", cur.Lang)
		}
	}
	if body.DisplayName != nil {
		cur.DisplayName = strings.TrimSpace(*body.DisplayName)
	}
	if body.Description != nil {
		cur.Description = strings.TrimSpace(*body.Description)
	}
	if body.Parameters != nil {
		cur.Parameters = xsql.JSONB{Data: body.Parameters}
	}
	if body.Output != nil {
		cur.Output = xsql.JSONB{Data: body.Output}
	}
	if body.TimeoutMS > 0 {
		cur.TimeoutMS = body.TimeoutMS
	}
	if body.MaxOutputBytes > 0 {
		cur.MaxOutputBytes = body.MaxOutputBytes
	}
	if body.Egress != nil {
		cur.Egress = *body.Egress
	}
	if body.ConfigFields != nil {
		cur.ConfigFields = xsql.JSONB{Data: body.ConfigFields}
	}
	if body.Configs != nil {
		// 打码串 = 保留旧值；显式空串 = 清空
		cur.Configs = xsql.JSONB{Data: mergeConfigs(cur.ConfigMap(), body.Configs)}
	}
	if body.Status != nil {
		cur.Status = *body.Status
	}
	// Upsert：盘上有目录、表里没行的工具也要能改状态与配置（缺行不该让编辑失败）。
	if err := storeFor(info.Schema).Upsert(cur); err != nil {
		xhttp.ErrJSON(c, "更新自定义工具失败: "+err.Error(), nil, "")
		return
	}
	if err := toolfile.WriteOf(config.Cfg.Agent.RootDir, info.Schema, cur); err != nil {
		// 写不成就把文件删掉：读路径文件优先，留着旧文件会让这次保存在下一轮读取里失效。
		_ = os.Remove(toolfile.Path(config.Cfg.Agent.RootDir, info.Schema, cur.Name))
		xhttp.ErrJSON(c, "设置已保存，但写入契约文件失败（已回退为表定义）: "+err.Error(), nil, "")
		return
	}
	if body.Script != nil {
		if err := writeEntry(info.Schema, cur, *body.Script); err != nil {
			xhttp.ErrJSON(c, "设置已保存，但写入实现文件失败: "+err.Error(), nil, "")
			return
		}
	} else if _, serr := os.Stat(entryPath(info.Schema, cur)); serr != nil {
		// 入口换了、新文件又不存在：补一份骨架。
		// 不管的话就是「保存成功、一跑就报入口不存在」—— 代码由文件面板写，
		// 表单这一侧不再携带代码，所以这个兜底必须留在后端。
		if werr := writeEntry(info.Schema, cur, tools.SkeletonForLang(cur.Lang)); werr != nil {
			xhttp.ErrJSON(c, "设置已保存，但入口文件不存在且补写骨架失败: "+werr.Error(), nil, "")
			return
		}
	}
	xhttp.OkJSON(c, "Success", gin.H{"tool": buildView(cur, toolfile.Contract{Source: "file"})}, "")
}

// DeleteCustomTool DELETE /customtools/:name
func DeleteCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	if err := storeFor(info.Schema).Delete(info.Schema, name); err != nil {
		xhttp.ErrJSON(c, "删除自定义工具失败: "+err.Error(), nil, "")
		return
	}
	// 代码目录一并删：留孤目录会让下一次同名创建困惑（也占盘）。
	dir := toolDir(info.Schema, name)
	if err := os.RemoveAll(dir); err != nil {
		xlog.Error("删除自定义工具目录失败 %s: %v", dir, err)
	}
	// 版本域同样按 name 键，必须一起清（DB cascade 删不掉盘）。
	PurgeToolVersions(info.Schema, name)
	// 授权行不主动清：ref 指向一个不存在的工具时，Resolve 会自然展开为空；
	// 保留它反而让"误删后重建"能直接恢复授权。
	xhttp.OkJSON(c, "Success", gin.H{"name": name}, "")
}

// runReq 试跑入参。
//
// TimeoutMS 缺省（0）= 用工具自己的 timeout_ms；给了也只能收紧或放宽到上限，
// 真正的上限仍是「工具声明 ∧ 本次运行」取小。
type runReq struct {
	Args      map[string]any `json:"args"`
	TimeoutMS int            `json:"timeout_ms"`
}

// maxTryRunTimeoutMS 试跑超时上限（与元工具 create_custom_tool 的 600s 同口径）。
const maxTryRunTimeoutMS = 600_000

// RunCustomTool POST /customtools/:name/run
//
// 「试跑」：用与线上完全相同的内核跑一次，把结果、stdout、stderr、退出码、耗时回给管理员。
//
// 三个刻意的取舍：
//  1. 跑的是**已保存**的代码（前端在表单有未保存改动时禁用按钮）——用临时文件跑未保存内容，
//     会让你看到的结果与线上跑的不是同一份代码，等于制造新的困惑；
//  2. 能力闸与准入一条不少（exec / egress / sandboxd 就绪）：试跑不是绕过平台策略的后门；
//  3. 执行失败返回 200 + ok=false，而不是 4xx —— 诊断信息（stderr、退出码）在 4xx 分支里
//     会被前端当成「请求错了」丢掉，而它恰恰是排查时最需要的东西。
func RunCustomTool(c *gin.Context) {
	info, ok := mustAuth(c)
	if !ok {
		return
	}
	name := c.Param("name")
	t, err := storeFor(info.Schema).Get(info.Schema, name)
	if err != nil {
		errToolNotFound(c)
		return
	}

	var body runReq
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrJSON(c, "Invalid request body", nil, "")
		return
	}
	args := body.Args
	if args == nil {
		args = map[string]any{}
	}

	// 能力闸：与执行层 requireExecCapability 同向，这里只多给一句「去哪儿开」。
	// 缺了它就是「管理端可以绕过平台策略」，那是最不该有的一条旁路。
	caps := capabilitypolicy.Snapshot(info.Schema)
	if !caps[capability.Exec] {
		xhttp.ErrJSON(c, fmt.Sprintf("本租户未开通 %s，自定义工具跑不起来（请在「工具能力」里开通）", capability.Exec), nil, "")
		return
	}
	if t.Egress && !caps[capability.Egress] {
		xhttp.ErrJSON(c, fmt.Sprintf("该工具声明了出网，但本租户未开通 %s", capability.Egress), nil, "")
		return
	}

	// 沙箱准入：与 exec / run_skill_script 同一条「不猜环境」判据。
	sb := tools.CurrentSandbox()
	if sb == nil {
		xhttp.ErrJSON(c, "沙箱未初始化（检查 sandbox.url 与 sandboxd）", nil, "")
		return
	}
	if !sandboxinfo.Ready() {
		xhttp.ErrJSON(c, "沙箱能力未知（sandboxd 未就绪）：不猜环境，暂不执行", nil, "")
		return
	}

	rootDir := config.Cfg.Agent.RootDir
	// 工作区放租户级 runs/ 下：在 root_dir 之内（sandboxd 的挂载根允许），
	// 且异常退出留下的目录会被启动清扫兜住（见 service/agent/janitor.go）。
	workspace := paths.ResolveRunsDir(rootDir, info.Schema, fmt.Sprintf("tryrun-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		xhttp.ErrJSON(c, "创建工作区失败: "+err.Error(), nil, "")
		return
	}
	defer os.RemoveAll(workspace)

	var timeout time.Duration
	if body.TimeoutMS > 0 {
		ms := body.TimeoutMS
		if ms > maxTryRunTimeoutMS {
			ms = maxTryRunTimeoutMS
		}
		timeout = time.Duration(ms) * time.Millisecond
	}

	warnings := make([]string, 0, 1)
	if t.Status != models.StatusNormal {
		warnings = append(warnings, "该工具当前不是「正常」状态，线上调用会被拒绝；这里只验证代码本身")
	}

	res, runErr := tools.RunCustomToolSpec(c.Request.Context(), tools.CustomToolRun{
		Spec:      tools.CustomToolSpecFromModel(t),
		ToolsRoot: paths.ResolveToolsDir(rootDir, info.Schema),
		Workspace: workspace,
		Sandbox:   sb,
		Timeout:   timeout,
		MaxOutput: tools.DefaultMaxOutput,
		Caps:      caps,
	}, args)

	durationMS := int64(0)
	if res != nil {
		durationMS = res.DurationMS
	}
	out := gin.H{"ok": runErr == nil, "duration_ms": durationMS}
	if res != nil {
		out["result"] = res.Result
		out["stdout"] = res.Stdout
		out["stderr"] = res.Stderr
		out["exit_code"] = res.ExitCode
		out["truncated"] = res.Truncated
		out["masked"] = res.Masked
	}
	if runErr != nil {
		out["error"] = runErr.Error()
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	xlog.Info("自定义工具试跑 schema=%s tool=%s ok=%v 耗时=%dms", info.Schema, name, runErr == nil, durationMS)
	xhttp.OkJSON(c, "Success", out, "")
}

// 下面两个小工具都只做本地文件系统，不绕 storage 抽象：
// 脚本执行本身也要求宿主路径可达（沙箱挂载源），S3 存储下这条链路本来就不可用。

func toolDir(schema, name string) string {
	return filepath.Join(paths.ResolveToolsDir(config.Cfg.Agent.RootDir, schema), name)
}

func entryPath(schema string, t *models.CustomTool) string {
	return filepath.Join(toolDir(schema, t.Name), t.Entry)
}

func readEntry(schema string, t *models.CustomTool) (string, error) {
	b, err := os.ReadFile(entryPath(schema, t))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func writeEntry(schema string, t *models.CustomTool, content string) error {
	p := entryPath(schema, t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// entryContent 创建时的实现文件内容：没给就用执行层那份骨架（唯一真源在 tools 包）。
func entryContent(p *string, t *models.CustomTool) string {
	if p != nil && strings.TrimSpace(*p) != "" {
		return *p
	}
	return tools.SkeletonForLang(t.Lang)
}

func defaultLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "js", "node":
		return "js"
	case "lua":
		return "lua"
	case "sh", "shell":
		return "sh"
	default:
		return "python"
	}
}

func defaultEntry(entry, lang string) string {
	if e := strings.TrimSpace(entry); e != "" {
		return e
	}
	switch defaultLang(lang) {
	case "js":
		return "main.js"
	case "lua":
		return "main.lua"
	case "sh":
		return "main.sh"
	}
	return "main.py"
}
