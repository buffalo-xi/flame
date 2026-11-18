package assetpack

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// secretKeyRe 键名命中即认为值可能是凭据。
//
// 宁可错杀：把一个正常字段清空，用户重新填一遍；漏掉一个 api_key，就是跨租户泄漏。
var secretKeyRe = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|passwd|credential|authorization)`)

// 延迟引用不是凭据：它只是名字/占位，真正的值要么在目标租户的
// {schema}.global_vars 里，要么由节点的输入变量在运行期取。反过来，若把它
// 当凭据清成空串，装到目标租户后引用就断了 —— 变成「静默坏」，比泄漏更难查。
//
// 三种写法的正则定义在 globalrefs.go（与需求清单扫描共用一份）：
// {{ .input.NAME }}（现行）、{{.global_vars.NAME}} 与 ${global_vars.NAME}（文档/存量）。

// isDeferredRef 判断某个字符串值是否整体就是一个引用（值不在包里）。
func isDeferredRef(s string) bool {
	return s != "" && globalRefExactRe.MatchString(s)
}

// containsDeferredRef 判断值里是否混着引用（如 "Bearer {{ .input.TOKEN }}"）。
func containsDeferredRef(s string) bool {
	return s != "" && (refInputRe.MatchString(s) || refTemplateRe.MatchString(s) || refDocRe.MatchString(s))
}

// StripSecrets 剥离快照 payload 里的敏感值，返回清洗后的副本与 warnings。
//
// 规则：
//   - custom_tool：`configs` 一律清成空对象。运行期配置值基本都是凭据（token、webhook、
//     数据库口令），跨租户带走等于泄漏；`config_fields`（声明有哪些配置项）保留，
//     装到目标租户后由使用方自己填。
//   - agent：`config` 内任意层级命中凭据键名的字符串值置空（模型 api_key、RPC 连接
//     口令都可能塞在这里），并逐条记 warning，方便审核页对照。
//   - workflow：`env_vars` 与 `diagram`（节点配置里的模型 api_key / webhook token
//     都在这里）两层都按同一套键名规则扫，同样逐条记 warning。
//   - skill：没有凭据字段，原样返回。
//
// 传入的 payload 不会被改动（先深拷贝），调用方可以继续用它做别的判断。
func StripSecrets(kind Kind, payload map[string]any) (map[string]any, []string) {
	out := deepCopyMap(payload)
	if out == nil {
		out = map[string]any{}
	}
	var warnings []string

	switch kind {
	case KindTool:
		if v, ok := payload["configs"]; ok {
			had := 0
			if m, isMap := v.(map[string]any); isMap {
				had = len(m)
			}
			out["configs"] = map[string]any{}
			if had > 0 {
				warnings = append(warnings, warn(
					"已清空 %d 项配置值：config_fields 声明保留，安装后需在目标租户重新填写", had))
			}
		}
	case KindAgent:
		if cfg, ok := out["config"].(map[string]any); ok {
			stripSecretKeys(cfg, "config", &warnings)
		}
	case KindWorkflow:
		// 两层分开处理，因为结构不同：
		//
		//   env_vars 是结构化表 {变量名: {type, value, description}} ——
		//     判定在 type 上，不在键名上。键名是变量名（API_KEY / DB_PASSWORD 这些
		//     名字本身就会命中凭据正则，按名字清会误伤），而值统一躺在 value 里，
		//     按名字扫根本扫不到 —— 这正是之前「env_vars 里的内联密钥其实没被清」
		//     的原因。现在按 type 判：
		//       secret → 清空 value（内联密钥，跨租户带走就是泄漏）
		//       global → 已废弃，value 是变量名就留着并提示迁移（清掉连指向谁都看不出来）
		//       其它   → 不动
		//
		//   diagram 是自由结构（节点配置），只能按键名扫。
		if m, ok := out["env_vars"].(map[string]any); ok {
			stripEnvVars(m, &warnings)
		}
		if m, ok := out["diagram"].(map[string]any); ok {
			stripSecretKeys(m, "diagram", &warnings)
		}
	}

	return out, warnings
}

// PreserveSecretKeys 覆盖安装时把「包内被清空的凭据键」换回目标已有值（原地改 incoming）。
//
// 上架时凭据值被清成空串，如果安装覆盖时直接整体替换，目标租户已经配好的
// api_key / token 就会被抹掉 —— 覆盖换的是内容，不是凭据。这与 placeTool 保留
// prev.Configs、工作流按变量名保留 env_vars 的 secret 是同一套约定，只是 agent 的
// config 是自由结构，没有 type 可依，只能退回按键名判断。
//
// 只处理「包内是空串 且 键名像凭据」这一种：包内给了非空值就按包走；
// 键名不像凭据的空字段不动（那可能是业务上真的该为空）。
// 递归下去，与 StripSecrets 的扫描深度保持一致。
//
// 代价与工作流的 secret 相同：靠「清空」删掉某项凭据是做不到的，只能删掉整项重建。
// 返回补回的键数量，供调用方在安装结果里说明。
func PreserveSecretKeys(incoming, target map[string]any) int {
	if len(incoming) == 0 || len(target) == 0 {
		return 0
	}
	kept := 0
	for key, val := range incoming {
		switch v := val.(type) {
		case map[string]any:
			if t, ok := target[key].(map[string]any); ok {
				kept += PreserveSecretKeys(v, t)
			}
		case string:
			if v != "" || !secretKeyRe.MatchString(key) {
				continue
			}
			if t, ok := target[key].(string); ok && t != "" {
				incoming[key] = t
				kept++
			}
		}
	}
	return kept
}

// stripEnvVars 按 type 处理环境变量表（见 StripSecrets 里 KindWorkflow 的说明）。
//
// 兼容两种写法：结构化 {type, value, description} 与早期的扁平 {"NAME": "值"}。
func stripEnvVars(envVars map[string]any, warnings *[]string) {
	for name, raw := range envVars {
		item, ok := raw.(map[string]any)
		if !ok {
			// 扁平写法：只能按键名兜底，并且必须排除全局变量引用
			s, isStr := raw.(string)
			if !isStr || s == "" || isDeferredRef(s) || !secretKeyRe.MatchString(name) {
				continue
			}
			envVars[name] = ""
			*warnings = append(*warnings, warn("env_vars.%s 疑似内联凭据，已清空", name))
			continue
		}

		switch kind, _ := item["type"].(string); kind {
		case "secret":
			if s, _ := item["value"].(string); s != "" {
				item["value"] = ""
				*warnings = append(*warnings,
					warn("env_vars.%s 是内联密钥，已清空（安装后需重新填写）", name))
			}
		case "global":
			// 已废弃的类型（全局变量改成节点输入的来源了）：value 仍是变量名、不是值，
			// 所以不清空 —— 清掉只会让老包连「原本指向谁」都看不出来。
			*warnings = append(*warnings,
				warn("env_vars.%s 使用已废弃的类型 global，导入后需改成节点输入变量的全局变量来源", name))
		}
	}
}

// stripSecretKeys 递归清空命中凭据键名的字符串值。
//
// 逐层走 map 与 []any（jsonb 解出来就是这两种），prefix 只用于 warning 里定位。
func stripSecretKeys(m map[string]any, prefix string, warnings *[]string) {
	for k, v := range m {
		key := prefix + "." + k
		switch val := v.(type) {
		case map[string]any:
			// 已废弃的环境变量声明 {type: "global", value: "NAME"}：value 是变量名不是值，
			// 整项跳过（stripEnvVars 会单独记一条迁移提示）。
			if kind, _ := val["type"].(string); kind == "global" {
				continue
			}
			stripSecretKeys(val, key, warnings)
		case []any:
			for i, item := range val {
				if sub, ok := item.(map[string]any); ok {
					stripSecretKeys(sub, key+"["+strconv.Itoa(i)+"]", warnings)
				}
			}
		default:
			if !secretKeyRe.MatchString(k) {
				continue
			}
			// 键名像凭据、但值是「引用」：留着。
			// 例如 {"api_key": "{{ .input.TOKEN }}"}：值来自节点输入变量的来源，
			// 不在包里；清掉它等于把引用改成空串，目标租户装上后是一个坏的空配置。
			if s, ok := v.(string); ok {
				if isDeferredRef(s) {
					continue
				}
				// 混合场景（"Bearer {{ .input.TOKEN }}"）：清掉会直接坏掉，
				// 保留但记 warning，让审核的人自己判断里面有没有内联凭据。
				if containsDeferredRef(s) {
					*warnings = append(*warnings,
						warn("%s 含引用（输入变量 / 全局变量），已保留（请确认其中没有内联凭据）", key))
					continue
				}
				if s == "" {
					continue // 本来就是空的，不记噪音
				}
			}
			m[k] = ""
			*warnings = append(*warnings, warn("%s 疑似凭据，已清空", key))
		}
	}
}

// deepCopyMap 深拷贝 JSON 形态的 map（只处理 map[string]any / []any / 标量）。
//
// jsonb 解出来的结构就这几种；不做通用反射拷贝，避免把 time.Time 之类拷坏。
func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return deepCopyMap(val)
	case []any:
		cp := make([]any, len(val))
		for i := range val {
			cp[i] = deepCopyValue(val[i])
		}
		return cp
	case json.RawMessage:
		// 生产形态：payload 里的 jsonb 列是数据库给的原始字节。
		// 不解码的话下面所有按 map 的判定（脱敏、依赖扫描）都会静默跳过 ——
		// 注释写着「已清空」，实际包里是明文。
		return decodeJSONValue(val)
	case []byte:
		return decodeJSONValue(val)
	default:
		return v
	}
}

// decodeJSONValue 把 jsonb 原始字节解成 map / []any / 标量。
//
// 解不开时返回空对象而不是原字节：这条路径上唯一的目的是「脱敏」，
// 带着看不懂的字节往前走，最可能的结果就是把它原样写进下载包。
func decodeJSONValue(b []byte) any {
	if len(b) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return map[string]any{}
	}
	return v
}
