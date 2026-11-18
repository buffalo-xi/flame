package plugin

import (
	"errors"
	"fmt"
)

// 调用错误码。与 flow 的错误码口径对齐（见 service/flow/core），
// 唯一的语义约定：**只有 CodeConfig 不触发兜底降级** ——
// 缺 key / 实现不存在这类问题，换一家实现也不会好。
const (
	CodeAuth    = "auth"    // 鉴权失败（key 错、过期、权限不足）
	CodeQuota   = "quota"   // 限流 / 配额耗尽
	CodeNetwork = "network" // 连不上、超时、5xx
	CodeConfig  = "config"  // 配置不对（缺字段、地址非法、参数不合法）
	CodeOther   = "other"
)

// CallError 一次插件调用失败，带可判定的错误码。
type CallError struct {
	Code    string
	Message string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("plugin call failed (%s): %s", e.Code, e.Message)
}

// Code 取错误的分类；非 CallError 归为 other。
func Code(err error) string {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return CodeOther
}

// normalizeCode 把插件回报的错误码收敛到已知集合（未知一律 other）。
func normalizeCode(code string) string {
	switch code {
	case CodeAuth, CodeQuota, CodeNetwork, CodeConfig:
		return code
	default:
		return CodeOther
	}
}
