package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 契约：业务编码进 error.data.error，协议码 error.code 保持标准语义不变。
// 前端按 data.error 翻译；外部 MCP 宿主按 code 判协议错。两者混用会同时坏掉两边。
func TestWriteRPCErrorCodeKeepsProtocolCode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRPCErrorCode(rec, 1, errCodeInvalidParams, "endpoint.not_exposed",
		map[string]string{"name": "poet"}, "服务端点未对外开放: %s", "poet")

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态应为 200（错误在 body 里），实际 %d", rec.Code)
	}

	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("error 字段缺失")
	}

	// 协议码必须还是 -32602，不能被业务编码顶替
	if resp.Error.Code != errCodeInvalidParams {
		t.Fatalf("error.code 应为 %d，实际 %d", errCodeInvalidParams, resp.Error.Code)
	}
	// message 保留中文原文：curl、日志、旧客户端都依赖它
	if resp.Error.Message != "服务端点未对外开放: poet" {
		t.Fatalf("error.message 被改动: %q", resp.Error.Message)
	}

	data, ok := resp.Error.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("error.data 应为对象，实际 %T", resp.Error.Data)
	}
	if got := data["error"]; got != "endpoint.not_exposed" {
		t.Fatalf("data.error 应为 endpoint.not_exposed，实际 %v", got)
	}
	params, ok := data["params"].(map[string]interface{})
	if !ok {
		t.Fatalf("data.params 应为对象，实际 %T", data["params"])
	}
	if got := params["name"]; got != "poet" {
		t.Fatalf("data.params.name 应为 poet，实际 %v", got)
	}
}

// params 为空时不输出该键（omitempty）：前端据「有没有 params」决定用不用模板渲染。
func TestWriteRPCErrorCodeOmitsEmptyParams(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRPCErrorCode(rec, 1, errCodeInternalError, "common.internal", nil, "内部错误")

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	errObj := body["error"].(map[string]interface{})
	data := errObj["data"].(map[string]interface{})
	if _, exists := data["params"]; exists {
		t.Fatalf("params 为空时不应出现该键，实际 %v", data)
	}
}
