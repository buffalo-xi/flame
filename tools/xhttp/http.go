// Package xhttp is the single HTTP response envelope for nexus.
//
// Two envelopes coexist and the boundary between them must hold:
//   - business results (success / bad request / over quota) → {code, msg, data, trace}, always HTTP
//     200, with success or failure in body.code (200 / 400 / 507). This is the existing contract with
//     desktop/src/platform/api.js — do not turn it into real HTTP status codes.
//   - transport / routing errors (401 / 403 / 503) → {error, message} with a real HTTP status code,
//     emitted by SendErrorResponse.
//
// Do not invent a third form. Writing c.JSON directly is only allowed for health / readiness probes
// that need bare JSON plus a real status code (see nexus/cmd/router.go).
package xhttp

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Business codes. These are values of body.code, which is a different thing from the HTTP status.
const (
	codeOK      = 200 // success
	codeInvalid = 400 // invalid arguments / business failure
	codeQuota   = 507 // quota exhausted (storage or session count)
)

// respond writes the {code, msg, data, trace} envelope.
//
// An empty trace falls back to Host+Path: while debugging you must be able to tell which instance a
// given path landed on. The HTTP status is always 200 — success and failure live in body.code.
func respond(c *gin.Context, code int, msg string, data interface{}, trace string) {
	if trace == "" {
		trace = c.Request.Host + c.Request.URL.Path
	}
	c.JSON(http.StatusOK, gin.H{
		"code":  code,
		"msg":   msg,
		"data":  data,
		"trace": trace,
	})
}

// OkJSON returns the success envelope (body.code = 200, HTTP 200).
// msg: response message
// data: response payload
// trace: caller path, defaulting to Host+Path
func OkJSON(c *gin.Context, msg string, data interface{}, trace string) {
	respond(c, codeOK, msg, data, trace)
}

// ErrQuota reports an exhausted quota (body.code = 507, HTTP still 200).
//
// Same HTTP 200 + body.code envelope as the other business errors, only the code differs: the frontend uses it to tell
// "storage / sessions full" apart from a plain argument error and can offer guidance such as "go clean
// up" instead of just a red bar.
//
// data should carry locator information: {"scope":"session","metric":"bytes","used":..,"limit":..}, and
// "evicted" (the files the pre-check already deleted) when there are any.
func ErrQuota(c *gin.Context, msg string, data interface{}, trace string) {
	respond(c, codeQuota, msg, data, trace)
}

// ErrQuotaCode is "over quota" plus a machine-readable code (body.code = 507, HTTP still 200).
//
// It differs from ErrQuota only in error / params: the two 507 cases (session storage full, session
// count full) need different copy, and the frontend tells them apart by error rather than guessing from
// body.code. data still carries scope/metric/used/limit, plus evicted when the pre-check deleted files before giving up.
func ErrQuotaCode(c *gin.Context, errCode string, params map[string]any, msg string, data interface{}, trace string) {
	if trace == "" {
		trace = c.Request.Host + c.Request.URL.Path
	}
	c.JSON(http.StatusOK, gin.H{
		"code":   codeQuota,
		"error":  errCode,
		"params": params,
		"msg":    msg,
		"data":   data,
		"trace":  trace,
	})
}

// ErrCode is a business error plus a machine-readable code (the frontend translates it, see
// nexus/docs/error-codes.md).
//
// On top of the plain business envelope it carries error (the code) and params (the values to render). msg
// is the diagnostic text (English) used by logs, curl and old frontends — translation is the frontend's
// job through error, and the backend does not care about language. When no entry is found the frontend
// falls back to msg, which is why msg must never be the only channel for user-visible copy.
//
// The parameter is named params rather than reusing data: in the existing envelope data means "payload
// for the other side", while this is "copy variables" (name, count, limit) and mixing them would
// mislead the frontend. params carries displayable scalars only — never internal addresses, SQL or stack
// traces.
func ErrCode(c *gin.Context, errCode string, params map[string]any, msg string, trace string) {
	if trace == "" {
		trace = c.Request.Host + c.Request.URL.Path
	}
	c.JSON(http.StatusOK, gin.H{
		"code":   codeInvalid,
		"error":  errCode,
		"params": params,
		"msg":    msg,
		"data":   nil,
		"trace":  trace,
	})
}

// ErrCodeData is a business error plus a machine-readable code and attached data (body.code = 400, HTTP
// still 200).
//
// It differs from ErrCode only in data: some errors carry structured information besides the copy (for
// example a name clash's suggested_name / can_overwrite, which turns the red bar into a "confirm
// overwrite" button). Those responses cannot use plain ErrCode, or data collapses to null and the UI
// degrades to "rename only".
func ErrCodeData(c *gin.Context, errCode string, params map[string]any, msg string, data interface{}, trace string) {
	if trace == "" {
		trace = c.Request.Host + c.Request.URL.Path
	}
	c.JSON(http.StatusOK, gin.H{
		"code":   codeInvalid,
		"error":  errCode,
		"params": params,
		"msg":    msg,
		"data":   data,
		"trace":  trace,
	})
}

// SendErrorResponse returns a transport / routing error (for front-gate failures).
//
// Unlike the business envelopes above, the body uses a top-level "error" (a reason enum string) plus
// "message" and does not reuse the "code" field, so it cannot be confused with the integer code of
// OkJSON / ErrCode or with JSON-RPC error.code. reason should be a snake_case enum: invalid_request /
// not_found / unauthorized / forbidden / rate_limited / internal_error.
func SendErrorResponse(c *gin.Context, status int, reason, message string) {
	c.JSON(status, gin.H{"error": reason, "message": message})
}
