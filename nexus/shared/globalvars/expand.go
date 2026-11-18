package globalvars

import (
	"fmt"
	"regexp"
	"strings"
)

// 文档里的引用写法：${global_vars.NAME}
//
// 为什么文档不用 Go 模板（{{.global_vars.X}}）：AGENT.md / SKILL.md 是文档，
// markdown 与代码示例里 {{ }} 太常见，一旦被当模板执行就是灾难（{{range}} 之类）。
// 这里只做一趟带命名空间的正则替换，命中范围可控。
var docRefRe = regexp.MustCompile(`\$\{global_vars\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand 把文档里的 ${global_vars.NAME} 替换成值。
//
// 语义要点：
//   - 没引用 → 原样返回，不查库（绝大多数文档里没有引用，别为此付一次 Resolve）；
//   - 变量不存在 → 返回 error。既不保留 ${...} 字面量（模型会照着读），也不换成空串
//     （配置缺失看起来像「用户就是这么配的」）；
//   - 引用了 kind=secret 的变量 → 返回 error。保存时有 ValidateDocRefs 拦，但资产包
//     安装进来的文档不经过那条校验，所以运行期这里必须再拦一次：展开之后就是一段
//     普通文本，分不出哪几个字来自密钥，而它会进 system prompt、session_records 与 SSE；
//   - schema 为空（平台级、单测）→ 原样返回：不属于任何租户，没有可解析的值。
func Expand(schema, text string) (string, error) {
	if text == "" || schema == "" || !strings.Contains(text, "${global_vars.") {
		return text, nil
	}

	vars, err := Resolve(schema)
	if err != nil {
		return "", err
	}
	secrets, err := SecretNames(schema)
	if err != nil {
		return "", err
	}

	var missing, blocked []string
	out := docRefRe.ReplaceAllStringFunc(text, func(m string) string {
		name := docRefRe.FindStringSubmatch(m)[1]
		value, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		if secrets[name] {
			blocked = append(blocked, name)
			return m
		}
		return fmt.Sprint(value)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("文档引用的全局变量不存在：%s（请先在「设置 → 全局变量」创建）",
			strings.Join(missing, "、"))
	}
	if len(blocked) > 0 {
		return "", fmt.Errorf("文档不能引用密钥类型的全局变量：%s（值会进模型上下文与运行记录）",
			strings.Join(blocked, "、"))
	}
	return out, nil
}

// DocRefs 返回文档里引用到的全局变量名（去重后按出现顺序）。
//
// 保存前的校验用：可以直接对着这份清单判「引用了不存在的变量」「引用了密钥类型」。
func DocRefs(text string) []string {
	if text == "" {
		return nil
	}
	matches := docRefRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m[1]]; ok {
			continue
		}
		seen[m[1]] = struct{}{}
		out = append(out, m[1])
	}
	return out
}
