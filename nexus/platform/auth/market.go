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

// marketTokenKey is the gin.Context key for the market token (it holds the token name, for logs and display).
const marketTokenKey = "platformMarketToken"

// AnonymousCaller marks an anonymous call (no token).
//
// It only happens when platform.market_settings.open = true: the token field of a catalog response returns it, so it is
// obvious at a glance that "this read came straight from a public market" rather than from some named token.
const AnonymousCaller = "anonymous"

// MarketTokenName returns the market token name used by this request (empty when unauthenticated).
func MarketTokenName(c *gin.Context) string {
	if v, ok := c.Get(marketTokenKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// HashMarketToken is the digest stored for a token (only the sha256 is kept; the plain value is returned once at creation).
func HashMarketToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// MarketTokenMiddleware is the server-to-server read-only authentication (/platform/market/* only).
//
// It is fully independent of the platform identity (the platform.users JWT):
//   - a platform identity is a "person" who can sign in to the console and change configuration;
//   - a market token is a "peer site" that can only read the reviewed asset catalog and packages; a leak only affects
//     exports and a single token can be revoked.
//
// Three outcomes:
//  1. token present -> checked against the table (only enabled counts as a hit); any failure is 401;
//  2. no token + market open (platform.market_settings.open) -> allowed, recorded as anonymous;
//  3. no token + not open -> 401.
//
// "Token present but invalid" gets no back door: open does not mean unvalidated - a mistyped token should be reported as 401
// at integration time rather than quietly passing as an anonymous call; otherwise "a token was configured but never took
// effect" would be impossible to find.
func MarketTokenMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			open, err := peers.MarketOpen()
			if err != nil {
				// Do not guess when the policy cannot be read: a 500 is more honest than "silently allow" or "pretend it is off".
				xhttp.SendErrorResponse(c, http.StatusInternalServerError, "internal_error",
					"failed to read the market egress policy: "+err.Error())
				c.Abort()
				return
			}
			if !open {
				xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized",
					"missing market token (if this server's market allows anonymous access, ask a platform admin to enable it under \"remote market -> market egress tokens\")")
				c.Abort()
				return
			}
			c.Set(marketTokenKey, AnonymousCaller)
			c.Next()
			return
		}
		t, err := peers.FindMarketTokenByHash(HashMarketToken(token))
		if err != nil {
			xhttp.SendErrorResponse(c, http.StatusInternalServerError, "internal_error", "failed to validate the market token")
			c.Abort()
			return
		}
		if t == nil {
			xhttp.SendErrorResponse(c, http.StatusUnauthorized, "unauthorized", "the market token is invalid or disabled")
			c.Abort()
			return
		}
		c.Set(marketTokenKey, t.Name)
		c.Next()
	}
}
