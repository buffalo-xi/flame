package secureval

import (
	"encoding/json"
	"fmt"
)

// 结构化环境变量表（flows.env_vars）的凭据字段处理。
//
// 形态：{变量名: {type, value, description}}，其中 type == "secret" 的 value 是凭据。
// 判定按 type 而不是按键名：键名是用户随手起的（API_KEY 也可能叫 A），
// 按名字扫一定会漏。
//
// 这个文件包了四件事，都是「同一个 JSON 形状」的不同视角，放一起才不会各自漂移：
//   - SealEnvVars / OpenEnvVars   落库加解密（确定性，见 detPrefix 的注释）
//   - MaskEnvVars                 出参掩码：接口不回明文，只留 has_value
//   - MergeEnvVarSecrets          入参合并：secret 留空 = 不改原值
//
// 扁平旧写法 {变量名: "值"} 没有 type，判不出来，一律原样保留 —— 存量就是这么存的，
// 猜一个类型去处理等于替用户改语义。

const envVarTypeSecret = "secret"

// SealEnvVars 加密 env_vars 里 type=secret 的值。
//
// 用确定性加密而不是随机 nonce：这块 JSON 整列参与版本比对
// （flows.env_vars 与 flow_versions.env_vars 在 SQL 里 IS DISTINCT FROM），
// 随机 nonce 会让「只改了描述」的保存被判成「变量也改了」。
func SealEnvVars(raw []byte) ([]byte, error) {
	return editSecretEnvVars(raw, func(_ string, fields map[string]json.RawMessage, value string) (bool, error) {
		dirty := false

		// has_value 是出参字段（MaskEnvVars 生它），不该落库：
		// 它由 value 推得，存进去只是多一份会漂移的真相 —— 有人直接改库清空 value 时，
		// 库里就会同时存在「空值 + has_value=true」这种自相矛盾的行。
		// 落库是所有写入口的必经之路（Create 与 Update 都过这里），在这里删最稳。
		if _, ok := fields["has_value"]; ok {
			delete(fields, "has_value")
			dirty = true
		}

		if value == "" {
			return dirty, nil
		}
		sealed, err := EncryptDeterministic(value)
		if err != nil {
			return false, err
		}
		if sealed == value {
			return dirty, nil // 已经是密文
		}
		fields["value"] = mustJSON(sealed)
		return true, nil
	})
}

// OpenEnvVars 把 env_vars 里的密文还原成明文。非密文（含存量明文）原样不动。
//
// 只在服务端内部用（执行上下文、资产包导出）；给前端的出参必须先过 MaskEnvVars。
func OpenEnvVars(raw []byte) ([]byte, error) {
	return editSecretEnvVars(raw, func(_ string, fields map[string]json.RawMessage, value string) (bool, error) {
		if value == "" {
			return false, nil
		}
		opened, err := Decrypt(value)
		if err != nil {
			return false, err
		}
		if opened == value {
			return false, nil // 存量明文
		}
		fields["value"] = mustJSON(opened)
		return true, nil
	})
}

// MaskEnvVars 出参用：type=secret 的值清空，另给 has_value 告诉前端「设过没」。
//
// 为什么不回掩码字符串（比如 ••••••）而回空串：前端保存时会把整份 env_vars 回传，
// 掩码字符串会被当成真实新值写回去。空串 + has_value 让「留空 = 不改」这条语义
// 在控制流上成立，而不是靠前端记得别提交掩码。
func MaskEnvVars(raw []byte) ([]byte, error) {
	return editSecretEnvVars(raw, func(_ string, fields map[string]json.RawMessage, value string) (bool, error) {
		fields["value"] = mustJSON("")
		fields["has_value"] = mustJSON(value != "")
		return true, nil
	})
}

// MergeEnvVarSecrets 入参用：secret 的 value 为空表示「不改原值」，从旧值补回来。
//
// 这条语义必须由后端兜住：界面上不回显明文，用户不改密码时提交的必然是空。
// 旧值不存在（或旧值也是空）时保持为空 —— 那是「新建了一个还没填值的变量」。
//
// 代价说清楚：secret 无法通过「清空」来删除值，要清掉只能删掉整个变量重建。
func MergeEnvVarSecrets(oldRaw, newRaw []byte) ([]byte, error) {
	old := secretValues(oldRaw)
	if len(old) == 0 {
		return newRaw, nil
	}
	return editSecretEnvVars(newRaw, func(name string, fields map[string]json.RawMessage, value string) (bool, error) {
		if value != "" {
			return false, nil
		}
		prev, ok := old[name]
		if !ok || prev == "" {
			return false, nil
		}
		fields["value"] = mustJSON(prev)
		return true, nil
	})
}

// secretValues 读出旧的 secret 明文值（按变量名索引）。解析失败一律当「没有旧值」：
// 这是合并路径上的辅助读，不该因为它让保存整个失败 —— 真正的错误会在写库前暴露。
func secretValues(raw []byte) map[string]string {
	out := make(map[string]string)
	if len(raw) == 0 {
		return out
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return out
	}
	for name, item := range top {
		_, value, ok := decodeSecretEntry(item)
		if !ok || value == "" {
			continue
		}
		out[name] = value
	}
	return out
}

// editSecretEnvVars 遍历 type=secret 的条目，把解码后的字段表交给 fn 原地改。
// fn 返回 true 表示「改了这一条」，用于决定是否需要重新序列化。
//
// 只重写改到的字段，其余（description 等）由 map[string]json.RawMessage 原样带过：
// 用结构体接会让「未来新增的字段」在往返中被悄悄丢掉。
func editSecretEnvVars(raw []byte, fn func(name string, fields map[string]json.RawMessage, value string) (bool, error)) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("env_vars 不是 JSON 对象: %w", err)
	}

	changed := false
	for name, item := range top {
		fields, value, ok := decodeSecretEntry(item)
		if !ok {
			continue // 扁平旧写法，或不是 secret：不碰
		}
		dirty, err := fn(name, fields, value)
		if err != nil {
			return nil, fmt.Errorf("处理环境变量 %s 失败: %w", name, err)
		}
		if !dirty {
			continue
		}
		patched, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		top[name] = patched
		changed = true
	}

	if !changed {
		return raw, nil
	}
	return json.Marshal(top)
}

// decodeSecretEntry 判断一项是不是 type=secret 的结构化条目，并取出字段表与当前值。
func decodeSecretEntry(item json.RawMessage) (map[string]json.RawMessage, string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item, &fields); err != nil {
		return nil, "", false
	}
	var typ string
	if v, ok := fields["type"]; ok {
		_ = json.Unmarshal(v, &typ)
	}
	if typ != envVarTypeSecret {
		return nil, "", false
	}
	var value string
	if v, ok := fields["value"]; ok {
		if err := json.Unmarshal(v, &value); err != nil {
			return nil, "", false // value 不是字符串（脏数据）：不碰
		}
	}
	return fields, value, true
}

// mustJSON 把字符串/布尔编成 JSON 字面量。这里的输入都是本包自己产生的
// （string / bool），编码不会失败，省掉错误分支。
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}
