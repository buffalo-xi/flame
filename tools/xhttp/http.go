// Package xhttp 统一 nexus 的 HTTP 响应信封。
//
// 两套信封并存，边界必须守住：
//   - 业务结果（成功 / 参数错误 / 配额不足）→ {code, msg, data, trace}，
//     恒为 HTTP 200，成败看 body.code（200 / 400 / 507）。这是与
//     desktop/src/platform/api.js 的既有约定，不要改成真实 HTTP 状态码。
//   - 传输层 / 路由层错误（401 / 403 / 503）→ {error, message}，配真实 HTTP
//     状态码，由 SendErrorResponse 发出。
//
// 不要再引入第三套写法。直接写 c.JSON 只允许用于健康 / 就绪探测这类需要
// 裸 JSON + 真实状态码的端点（见 nexus/cmd/router.go）。
package xhttp

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 业务码。注意这是 body.code 的取值，与 HTTP 状态码是两回事。
const (
	codeOK      = 200 // 成功
	codeInvalid = 400 // 参数错误 / 业务失败
	codeQuota   = 507 // 配额不足（空间 / 会话数已满）
)

// respond 写出 {code, msg, data, trace} 信封。
//
// trace 为空时取 Host+Path：排障时要能区分同一条路径落在哪个实例上。
// HTTP 状态恒为 200 —— 业务成败由 body.code 表达。
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

// OkJSON 返回成功信封（body.code = 200，HTTP 200）。
// msg: 响应消息
// data: 响应数据
// trace: 调用路径，为空时取 Host+Path
func OkJSON(c *gin.Context, msg string, data interface{}, trace string) {
	respond(c, codeOK, msg, data, trace)
}

// ErrJSON 返回业务错误信封：HTTP 200 + body.code = 400。
//
// 注意 HTTP 状态码是 200 而非 400 —— 这是与前端约定的信封，前端读 body.code
// 判断成败；按「HTTP 400」写判断会永远不成立。
// msg: 错误消息
// data: 错误相关数据
// trace: 调用路径，为空时取 Host+Path
func ErrJSON(c *gin.Context, msg string, data interface{}, trace string) {
	respond(c, codeInvalid, msg, data, trace)
}

// ErrQuota 配额不足（body.code = 507，HTTP 仍为 200）。
//
// 与 ErrJSON 同为 HTTP 200 + body.code 的信封，只是 code 用 507：
// 前端据此把「空间/会话数已满」与普通参数错误区分开，给出「去清理」这类引导，
// 而不是只弹一个红条。
//
// data 建议带上定位信息：{"scope":"session","metric":"bytes","used":..,"limit":..}
func ErrQuota(c *gin.Context, msg string, data interface{}, trace string) {
	respond(c, codeQuota, msg, data, trace)
}

// ErrCode 业务错误 + 机器可读编码（前端据此翻译，见 nexus/docs/error-codes.md）。
//
// 与 ErrJSON 只差一点：多带 error（编码）与 params（渲染参数）。
// msg 仍然返回中文原文 —— 日志、curl、旧前端都用它；翻译由前端按 error 查表完成，
// 后端不关心 language。查不到对应语言条目时前端回退 msg，所以这个函数可以渐进替换。
//
// 参数名用 params 而不是复用 data：data 在既有信封里是「给对方的数据」，
// 这里是「文案变量」（名字、数量、上限），混用会让前端误判。
// params 只放可展示的标量，不要放内网地址、SQL 或堆栈。
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

// SendErrorResponse 返回传输层/路由层错误（前置校验失败时专用）。
//
// 与上面的业务信封不同：返回体用顶层 "error"（字符串枚举 reason）+ "message"，
// 不复用 "code" 字段，避免与 OkJSON/ErrJSON 的整数 code 以及 JSON-RPC error.code 混淆。
// reason 应为 snake_case 枚举：invalid_request / not_found / unauthorized /
// forbidden / rate_limited / internal_error。
func SendErrorResponse(c *gin.Context, status int, reason, message string) {
	c.JSON(status, gin.H{"error": reason, "message": message})
}
