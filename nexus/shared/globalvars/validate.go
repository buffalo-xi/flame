package globalvars

import (
	"fmt"

	repo "flame/nexus/shared/repository/globalvars"
)

// ValidateDocRefs 保存前的校验：文档（AGENT.md / SKILL.md）里引用的全局变量不能是密钥类型。
//
// 只拦这一条，不拦「引用了还不存在的变量」：
//   - 密钥会进模型上下文、进 session_records、进 SSE —— 一旦写进提示词就是泄漏，
//     而当事人往往以为「引用的是变量名，不是值」；
//   - 不存在的变量不拦：先写文档再建变量是正常顺序，而且运行期取不到会硬失败、
//     错误信息里带着变量名，不会静默跑错。
//
// schema 为空（平台级）直接放行：没有租户就没有全局变量可查。
func ValidateDocRefs(schema, text string) error {
	if schema == "" || text == "" {
		return nil
	}
	refs := DocRefs(text)
	if len(refs) == 0 {
		return nil
	}

	// 直接查库而不是走 Resolve：Resolve 有 5s 缓存，刚改成密钥的变量可能还不在缓存里，
	// 而这种校验「宁可刚一点」—— 缓存过期前放过去一条，就是一次真实泄漏。
	store := repo.NewRepository()
	for _, name := range refs {
		v, err := store.Get(schema, name)
		if err != nil {
			return fmt.Errorf("校验全局变量 %s 失败: %w", name, err)
		}
		if v == nil {
			continue // 还没建：允许先写文档
		}
		if v.Kind == repo.KindSecret {
			return fmt.Errorf(
				"不能引用密钥类型的全局变量 %s：它会进模型上下文与运行记录（${global_vars.%s}）。"+
					"密钥请只用于连接 / 工具配置，或改用 text 类型的变量", name, name)
		}
	}
	return nil
}
