package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"flame/nexus/shared/repository/peers"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// marketTokenKey 市场令牌在 gin.Context 中的键（记的是令牌名，用于日志/展示）。
const marketTokenKey = "platformMarketToken"

// AnonymousCaller 匿名调用（不带令牌）的标识。
//
// 只有 platform.market_settings.open = true 时才会出现：目录响应里的 token 字段会回它，
// 让人一眼看出「这条是公开市场被直接读走的」，而不是某个令牌名。
const AnonymousCaller = "anonymous"

// MarketTokenName 取当前请求用的市场令牌名（未认证为空）。
func MarketTokenName(c *gin.Context) string {
	if v, ok := c.Get(marketTokenKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// HashMarketToken 令牌落库前的摘要（只存 sha256，明文只在创建时回一次）。
func HashMarketToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// MarketTokenMiddleware 站到站只读鉴权（/platform/market/* 专用）。
//
// 与平台身份（platform.users 的 JWT）完全独立：
//   - 平台身份是「人」，能登录管理台、能改配置；
//   - 市场令牌是「对端站点」，只能读已审核资产目录与资产包，泄露也只影响导出，单条可吊销。
//
// 三种结果：
//  1. 带令牌 → 查表校验（enabled 才算命中），失败一律 401；
//  2. 不带令牌 + 市场开放（platform.market_settings.open）→ 放行，记为 anonymous；
//  3. 不带令牌 + 未开放 → 401。
//
// 「带了令牌但无效」不开后门：开放不等于不校验 —— 一笔写错的令牌应当在接入时
// 就 401 报出来，而不是被当成匿名调用悄悄跑通；否则「配了令牌却一直没生效」
// 这种事永远查不到。
func MarketTokenMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			open, err := peers.MarketOpen()
			if err != nil {
				// 读不到策略时不猜：500 比「静默放行」和「假装没开」都诚实。
				xhttp.SendErrorResponse(c, http.StatusInternalServerError, "internal_error",
					"读取市场出口策略失败: "+err.Error())
				c.Abort()
				return
			}
			if !open {
				xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized",
					"缺少市场令牌（若本机市场开放匿名访问，请平台管理员在「远端市场 → 市场出口令牌」里打开）")
				c.Abort()
				return
			}
			c.Set(marketTokenKey, AnonymousCaller)
			c.Next()
			return
		}
		t, err := peers.FindMarketTokenByHash(HashMarketToken(token))
		if err != nil {
			xhttp.SendErrorResponse(c, http.StatusInternalServerError, "internal_error", "校验市场令牌失败")
			c.Abort()
			return
		}
		if t == nil {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "市场令牌无效或已停用")
			c.Abort()
			return
		}
		c.Set(marketTokenKey, t.Name)
		c.Next()
	}
}
