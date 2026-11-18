package executors

import (
	"bytes"
	"encoding/json"
	"errors"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/globalvars"
	"fmt"
	"strings"
	"sync"
	"text/template"
)

// tmplCache 模板缓存，避免重复 Parse
var tmplCache sync.Map

// tmplFuncs 模板自定义函数集
var tmplFuncs = template.FuncMap{
	"json": func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	},
	"truncate": func(n int, s string) string {
		r := []rune(s)
		if len(r) > n {
			return string(r[:n])
		}
		return s
	},
	"default": func(def, v any) any {
		if v == nil {
			return def
		}
		switch val := v.(type) {
		case string:
			if val == "" {
				return def
			}
		case bool:
			if !val {
				return def
			}
		}
		return v
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"trim":  strings.TrimSpace,
}

const (
	// maxTemplateOutputBytes 单次模板渲染的输出上限。
	// text/template 的 Execute 是增量写出的，Write 返回 error 会立即中止渲染 ——
	// 因此这道闸能真正防住「渲染出超大字符串撑爆内存」（加超时做不到，因为同进程
	// 的 Go 模板渲染本身不可中断）。
	maxTemplateOutputBytes = 1 << 20 // 1 MiB
	// maxTemplateDataNodes 模板可见数据（map/slice 元素 + 标量）总节点数上限。
	// {{range}} 迭代次数上界 ≈ 元素个数，压住输入规模即压住渲染规模的源头。
	maxTemplateDataNodes = 50000
	// maxTemplateDataDepth 数据嵌套深度上限，防止遍历/渲染被超深结构放大。
	maxTemplateDataDepth = 32
)

// errTemplateOutputTooLarge 输出超限哨兵错误。
var errTemplateOutputTooLarge = errors.New("template output exceeds size limit")

// limitedWriter 是带上限的 io.Writer：累计写入超过 limit 后返回错误。
type limitedWriter struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		w.exceeded = true
		return 0, errTemplateOutputTooLarge
	}
	return w.buf.Write(p)
}

func (w *limitedWriter) String() string { return w.buf.String() }

// checkTemplateData 限制模板可见数据的节点数与嵌套深度。
//
// 只做规模封顶，不提供时间维度的硬超时 —— 同进程 Go 模板渲染不可中断，
// 畸形模板的墙钟时间只能靠进程级隔离约束。
func checkTemplateData(data any) error {
	n := 0
	var walk func(v any, depth int) error
	walk = func(v any, depth int) error {
		if depth > maxTemplateDataDepth {
			return fmt.Errorf("template data nested too deep (> %d)", maxTemplateDataDepth)
		}
		n++
		if n > maxTemplateDataNodes {
			return fmt.Errorf("template data too large (> %d nodes)", maxTemplateDataNodes)
		}
		switch t := v.(type) {
		case map[string]any:
			for _, e := range t {
				if err := walk(e, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, e := range t {
				if err := walk(e, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(data, 0)
}

// RenderTemplate 渲染模板字符串并直接返回字符串（不做 JSON 解码），使用缓存避免重复 Parse。
//
// 带两道规模护栏（Code / Result / Progress / binding transform 共用本函数）：
//   - 输入：checkTemplateData 限制可见数据的节点数与嵌套深度；
//   - 输出：limitedWriter 超过 maxTemplateOutputBytes 即中止渲染。
func RenderTemplate(tmplStr string, data map[string]any) (string, error) {
	if err := checkTemplateData(data); err != nil {
		return "", err
	}
	t, err := getOrParseTemplate(tmplStr)
	if err != nil {
		return "", err
	}
	w := &limitedWriter{limit: maxTemplateOutputBytes}
	if err := t.Execute(w, data); err != nil {
		if w.exceeded {
			return "", errTemplateOutputTooLarge
		}
		return "", err
	}
	return w.String(), nil
}

// getOrParseTemplate 从缓存获取或解析模板
func getOrParseTemplate(tmplStr string) (*template.Template, error) {
	if cached, ok := tmplCache.Load(tmplStr); ok {
		return cached.(*template.Template), nil
	}
	t, err := template.New("tmpl").Funcs(tmplFuncs).Parse(tmplStr)
	if err != nil {
		return nil, err
	}
	tmplCache.Store(tmplStr, t)
	return t, nil
}

// BuildScriptTemplateContext 构建 template 脚本的三段上下文：
//
//	.input  — 节点输入（全局变量也在这里：它是 inputs 的一种来源）
//	.local  — 局部变量（SnapshotVars 浅拷贝快照）
//	.output — 输出变量（SnapshotVars 浅拷贝快照）
//
// 全局变量不再单独注入一段（曾经是 .global_vars）：它和 argument / env / flow /
// node / builtin 一样是节点输入的来源，取值在 GetVariables 里完成。少一段上下文
// 就少一条「模板里能偷偷拿到全局值」的暗通道 —— 节点配置与输入变量表现在描述
// 的是同一件事。
func BuildScriptTemplateContext(nr *flow.NodeResult, inputs map[string]any) map[string]any {
	localCopy, outputCopy := nr.SnapshotVars()
	return map[string]any{
		"input":  inputs,
		"local":  localCopy,
		"output": outputCopy,
	}
}

// WarnSecretPromptRefs 对「提示词里引用了密钥类型的全局变量」记一条告警（不阻断）。
//
// 判定看的是**来源**而不是文本：全局变量现在经节点输入（{{ .input.xxx }}）进入，
// 提示词里已经找不到变量名。所以先按 config.inputs 里 source_type == "global" 的项
// 反查哪些 key 来自 kind == secret 的变量，再看提示词有没有引用这些 key。
//
// 为什么只在提示词上提示：节点配置里用密钥是合法用法（http 的 Authorization 头），
// 真正多一层泄漏面的是提示词 —— 渲染结果会进模型上下文、session_records 与 SSE。
func WarnSecretPromptRefs(nr *flow.NodeResult, cfg map[string]any, texts ...string) {
	if nr == nil || nr.Ctx == nil || nr.Ctx.Schema == "" {
		return
	}
	inputs, _ := cfg["inputs"].(map[string]any)
	if len(inputs) == 0 || len(texts) == 0 {
		return
	}

	// 先收「来自全局变量」的输入名，读不到密钥清单就什么都不提示：
	// 这是 advisory 的告警，不该因为查库失败把节点带崩。
	fromGlobal := make(map[string]string, len(inputs))
	for name, raw := range inputs {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if sourceType, _ := item["source_type"].(string); sourceType != "global" {
			continue
		}
		if key, _ := item["source_key"].(string); key != "" {
			fromGlobal[name] = key
		}
	}
	if len(fromGlobal) == 0 {
		return
	}

	secrets, err := globalvars.SecretNames(nr.Ctx.Schema)
	if err != nil {
		return
	}
	for inputName, globalName := range fromGlobal {
		if !secrets[globalName] {
			continue
		}
		for _, text := range texts {
			if !refersToInput(text, inputName) {
				continue
			}
			nr.WarnOnce("secret-prompt:"+inputName,
				"提示词引用了密钥类型的全局变量 %s（输入变量 %s）：渲染结果会进模型上下文与运行记录，密钥建议只用于连接 / 工具配置",
				globalName, inputName)
			break
		}
	}
}

// refersToInput 判断一段模板文本里是否引用了 {{ .input.<name> }}。
//
// 不用整段正则：模板里可能带管道（{{ .input.token | upper }}）或属性访问
// （{{ .input.token.len }}），只在名字后面要求「不是标识符字符」即可覆盖。
// 误报的代价只是一条告警，漏报才是问题，所以判定偏宽。
func refersToInput(text, name string) bool {
	needle := ".input." + name
	for i := 0; ; {
		idx := strings.Index(text[i:], needle)
		if idx < 0 {
			return false
		}
		end := i + idx + len(needle)
		if end >= len(text) {
			return true
		}
		if c := text[end]; c != '_' &&
			(c < '0' || c > '9') &&
			(c < 'a' || c > 'z') &&
			(c < 'A' || c > 'Z') {
			return true
		}
		i = end
	}
}

// ExecuteTemplateScript 执行 template 脚本，data 由调用方构建（三段上下文）
func ExecuteTemplateScript(script string, data map[string]any) (any, error) {
	rendered, err := RenderTemplate(script, data)
	if err != nil {
		return nil, fmt.Errorf("template render: %w", err)
	}
	// UseNumber 保持数字为 json.Number，与 lua 一致
	dec := json.NewDecoder(strings.NewReader(rendered))
	dec.UseNumber()
	var result any
	if err := dec.Decode(&result); err != nil {
		return nil, fmt.Errorf("template result json decode: %w", err)
	}
	return result, nil
}
