package assetpack

import (
	"fmt"
	"strings"

	"flame/nexus/shared/paths"
)

// MaxNameLen 名字长度上限（与 paths.ValidName 一致，也是目录名的实际上限）。
const MaxNameLen = 64

// ValidateName 校验上架/安装时的实体名。
//
// 与 paths.ValidName 同一条判据（字母数字与横线、不以横线开头、≤64）：
// 名字最终会变成目录名（tenants/<schema>/skills/<name>），宽松的名字等于给存储层留注入面。
func ValidateName(name string) error {
	if !paths.ValidName(name) {
		return fmt.Errorf("the name %q is not valid: only letters, digits and dashes, no leading dash, at most %d characters",
			name, MaxNameLen)
	}
	return nil
}

// SuggestName 在已占用的名字集合里给一个不冲突的建议名：name-2、name-3 …
//
// 冲突时**不静默改名**：悄悄装成 `name-2`，用户会以为自己装的就是原来那个，之后
// 授权、绑定、排查全对不上。正确做法是接口回 409 带上这个建议，由用户确认后用
// name= 参数重试。
func SuggestName(base string, taken []string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "asset"
	}

	set := make(map[string]struct{}, len(taken))
	for _, t := range taken {
		if t = strings.TrimSpace(t); t != "" {
			set[strings.ToLower(t)] = struct{}{}
		}
	}
	if _, clash := set[strings.ToLower(base)]; !clash {
		return base
	}

	// taken 是有限集合，所以 i 一旦越过 len(set)+1 必然有空位；上界只是防御。
	for i := 2; i <= len(set)+2; i++ {
		suffix := fmt.Sprintf("-%d", i)
		stem := base
		if len(stem)+len(suffix) > MaxNameLen {
			stem = stem[:MaxNameLen-len(suffix)]
		}
		cand := stem + suffix
		if _, clash := set[strings.ToLower(cand)]; !clash {
			return cand
		}
	}
	return base // 理论上不可达
}
