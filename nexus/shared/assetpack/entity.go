package assetpack

import "encoding/json"

// PortableSkill 把 skills 行（`SELECT to_jsonb(s) FROM <schema>.skills s` 的结果）收敛成
// 可跨租户的快照 payload。
//
// 白名单而不是黑名单：新增列默认**不会**被带出去，漏掉一列只是功能少了，多带一列可能是
// 身份（id / agent_id）或审计字段（created_by / 时间戳）—— 那些在目标租户必须重新生成，
// 照搬只会让两边 id 对不上。
//
// status 刻意不搬：装上去是启用还是停用，由安装请求决定（默认 1），不能由源租户的状态
// 替目标租户做主。
func PortableSkill(row map[string]any) map[string]any {
	out := make(map[string]any, 8)
	for _, k := range []string{
		"name", "description", "metadata",
		"user_invocable", "command_dispatch", "command_tool",
		"restricted",
	} {
		if v, ok := row[k]; ok {
			out[k] = v
		}
	}
	return out
}

// EncodeJSONB 把任意值编成可直接塞进 jsonb 参数的字节串（nil → "{}"）。
//
// 快照 payload / manifest 都要落 jsonb；统一走这里，避免各处对 nil 的处理不一致
// （sql 层把 nil []byte 当 SQL NULL，而列有 NOT NULL 约束）。
func EncodeJSONB(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}

// DecodeJSONB 把 jsonb 列读成 map（NULL / 空 → 空 map）。
func DecodeJSONB(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}
