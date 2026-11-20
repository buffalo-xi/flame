package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Contract: the business code goes into error.data.error and the protocol code in error.code keeps its
// standard meaning. The frontend translates by data.error; an external MCP host reads code as the
// protocol error. Mixing the two breaks both sides at once.
func TestWriteRPCErrorCodeKeepsProtocolCode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRPCErrorCode(rec, 1, errCodeInvalidParams, "endpoint.not_exposed",
		map[string]string{"name": "poet"}, "service endpoint is not exposed: %s", "poet")

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status should be 200 (the error lives in the body), got %d", rec.Code)
	}

	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("the response is not valid JSON: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("the error field is missing")
	}

	// The protocol code must still be -32602, never replaced by the business code.
	if resp.Error.Code != errCodeInvalidParams {
		t.Fatalf("error.code should be %d, got %d", errCodeInvalidParams, resp.Error.Code)
	}
	// The message is the diagnostic text handed to the caller as is.
	if resp.Error.Message != "service endpoint is not exposed: poet" {
		t.Fatalf("error.message was changed: %q", resp.Error.Message)
	}

	data, ok := resp.Error.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("error.data should be an object, got %T", resp.Error.Data)
	}
	if got := data["error"]; got != "endpoint.not_exposed" {
		t.Fatalf("data.error should be endpoint.not_exposed, got %v", got)
	}
	params, ok := data["params"].(map[string]interface{})
	if !ok {
		t.Fatalf("data.params should be an object, got %T", data["params"])
	}
	if got := params["name"]; got != "poet" {
		t.Fatalf("data.params.name should be poet, got %v", got)
	}
}

// Empty params omit the key (omitempty): the frontend uses "is params present" to decide whether to
// render the template.
func TestWriteRPCErrorCodeOmitsEmptyParams(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRPCErrorCode(rec, 1, errCodeInternalError, "common.internal", nil, "internal error")

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the response is not valid JSON: %v", err)
	}
	errObj := body["error"].(map[string]interface{})
	data := errObj["data"].(map[string]interface{})
	if _, exists := data["params"]; exists {
		t.Fatalf("the params key must not appear when empty, got %v", data)
	}
}
