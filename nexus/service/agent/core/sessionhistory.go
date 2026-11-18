package core

import (
	"encoding/json"

	"flame/nexus/service/llm/gateway"
	"flame/nexus/shared/models"
)

// RecordUserContent 取一条会话记录里的用户输入正文（input jsonb 存的是 {"content":"..."}）。
//
// 解析失败或缺字段时回退 "{}"（与改造前的行为一致）：历史回放宁可显示一个占位，
// 也不要因为某条脏数据让整段上下文读不出来。
func RecordUserContent(rec models.SessionRecord) string {
	if len(rec.Input) == 0 {
		return "{}"
	}
	var in map[string]any
	if err := json.Unmarshal(rec.Input, &in); err != nil {
		return "{}"
	}
	if c, ok := in["content"].(string); ok {
		return c
	}
	return "{}"
}

// RecordsToMessages 把会话记录转成 LLM 消息（user/assistant 交替）。
//
// 一条记录 = 一轮运行（入参 + 最终结果），不是一条聊天消息，所以这里拆成两条。
// 放在 core 而不是入口层：Web 对话与协议端点必须用同一份转换，否则同一个会话
// 从两个入口读出来的历史会长得不一样。
func RecordsToMessages(records []models.SessionRecord) []gateway.Message {
	msgs := make([]gateway.Message, 0, len(records)*2)
	for _, rec := range records {
		msgs = append(msgs, gateway.Message{Role: "user", Content: RecordUserContent(rec)})
		if rec.Output != "" {
			msgs = append(msgs, gateway.Message{Role: "assistant", Content: rec.Output})
		}
	}
	return msgs
}
