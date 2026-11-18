package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"flame/nexus/shared/auth"
	svcauthbindings "flame/nexus/shared/repository/svcauthbindings"
	"flame/nexus/shared/svcpath"
	sharedtenants "flame/nexus/shared/tenants"
	"flame/tools/xsql"

	"github.com/golang-jwt/jwt/v5"
)

// errNoAuth 表示请求未携带任何凭证（需返回 401 + WWW-Authenticate）
var errNoAuth = fmt.Errorf("no authorization provided")

// resolveTenant 从请求路径取出租户标识（即 Postgres schema 名）。
// 路径形态：POST /service/rpc/{schema}，由路由 /rpc/:schema 注入上下文（见 svcpath）。
func resolveTenant(r *http.Request) (*auth.AuthInfo, error) {
	schema := strings.TrimSpace(svcpath.FromContext(r.Context()))
	if schema == "" {
		return nil, fmt.Errorf("missing tenant schema in path: /service/rpc/<schema>")
	}
	// 第一道：schema 名只允许字母数字下划线，避免 SQL 注入
	if !isValidSchemaName(schema) {
		return nil, fmt.Errorf("invalid tenant schema name %q", schema)
	}
	// 第二道：注册表白名单 —— 只接受「已注册且 active」的租户（停用租户立即失效）。
	// 查询失败一律拒绝（fail-closed），不要当成"放行"或"未注册"静默处理。
	active, terr := sharedtenants.IsActive(schema)
	if terr != nil {
		return nil, fmt.Errorf("tenant lookup failed: %w", terr)
	}
	if !active {
		return nil, fmt.Errorf("tenant %q not found or inactive", schema)
	}
	return &auth.AuthInfo{Schema: schema}, nil
}

// isValidSchemaName 白名单校验租户标识（必须是合法标识符，避免拼接 SQL 注入）
func isValidSchemaName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// authenticate 校验请求凭证，成功返回带用户身份的 AuthInfo。
// 无凭证时返回 errNoAuth（调用方应据此回 401 + WWW-Authenticate 指引）。
func authenticate(authInfo *auth.AuthInfo, r *http.Request) (*auth.AuthInfo, error) {
	bearer := extractBearer(r)
	if bearer == "" {
		return nil, errNoAuth
	}

	// 靠值形态分流：JWT（eyJ 开头、三段）走 oauth，否则 apikey
	if looksLikeJWT(bearer) {
		return verifyOAuth(authInfo, bearer)
	}
	return verifyAPIKey(authInfo, bearer)
}

// extractBearer 取出 Authorization: Bearer <token> 中的 token
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// looksLikeJWT 启发式判断 token 是否 JWT（eyJ 开头、含两个句点）
func looksLikeJWT(token string) bool {
	if !strings.HasPrefix(token, "eyJ") {
		return false
	}
	dots := strings.Count(token, ".")
	return dots == 2
}

// verifyAPIKey 校验 apikey：方式开关启用 + 在 svc_auth_bindings 命中绑定，返回绑定用户。
func verifyAPIKey(authInfo *auth.AuthInfo, token string) (*auth.AuthInfo, error) {
	cfg, err := GetServerConfig(authInfo, "apikey")
	if err != nil {
		return nil, fmt.Errorf("apikey auth not configured: %w", err)
	}
	if !cfg.IsActive {
		return nil, fmt.Errorf("apikey auth disabled")
	}

	// 明文 token 直接查绑定表（apikey 的 external_issuer 为空串）
	bi, err := authorizeBinding(authInfo.Schema, "apikey", "", token)
	if err != nil {
		return nil, err
	}
	return authInfoFromBinding(authInfo.Schema, bi), nil
}

// verifyOAuth 用 JWKS 验签 JWT，校验 audience / scope，并在 svc_auth_bindings 命中所属用户。
func verifyOAuth(authInfo *auth.AuthInfo, token string) (*auth.AuthInfo, error) {
	cfg, err := GetServerConfig(authInfo, "oauth")
	if err != nil {
		return nil, fmt.Errorf("oauth auth not configured: %w", err)
	}
	if !cfg.IsActive {
		return nil, fmt.Errorf("oauth auth disabled")
	}
	var oc OAuthConfig
	if err := unmarshalAuthConfig(cfg.AuthConfig, &oc); err != nil {
		return nil, fmt.Errorf("invalid oauth config: %w", err)
	}
	if oc.JWKSURL == "" {
		return nil, fmt.Errorf("oauth jwks_url not configured")
	}

	keyFunc, err := jwksKeyFunc(oc.JWKSURL)
	if err != nil {
		return nil, err
	}

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, keyFunc, jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}))
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("invalid oauth token: %w", err)
	}

	// audience 校验
	if oc.Audience != "" {
		if aud, ok := claims["aud"]; ok {
			if !containsAudience(aud, oc.Audience) {
				return nil, fmt.Errorf("oauth token audience mismatch")
			}
		} else {
			return nil, fmt.Errorf("oauth token missing audience")
		}
	}

	// scope 校验（required_scope 需出现在 token 的 scope 中）
	if oc.RequiredScope != "" {
		scp, _ := claims["scope"].(string)
		if !containsScope(scp, oc.RequiredScope) {
			return nil, fmt.Errorf("oauth token missing required scope %q", oc.RequiredScope)
		}
	}

	// 从 token 提取外部身份（iss + sub）→ 查 svc_auth_bindings
	iss, _ := claims["iss"].(string)
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("oauth token missing sub")
	}
	bi, err := authorizeBinding(authInfo.Schema, "oauth", iss, sub)
	if err != nil {
		return nil, err
	}
	return authInfoFromBinding(authInfo.Schema, bi), nil
}

// authorizeBinding 解析绑定并做唯一一项校验：绑定命中的账号处于 active。
// 刻意不判角色 —— 绑定只是给服务调用一个身份（审计用），不代表权限；
// 账号被停用/锁定后其绑定立即失效（无需删绑定），账号恢复后自动生效。
//
// 返回值带上该凭证的端点访问范围（restrict_targets / 白名单），由调用方装进 AuthInfo。
func authorizeBinding(schema, authType, issuer, subject string) (*svcauthbindings.BindingIdentity, error) {
	bi, err := svcauthbindings.NewRepository().ResolveBindingIdentity(schema, authType, issuer, subject)
	if err != nil {
		return nil, fmt.Errorf("resolve %s binding: %w", authType, err)
	}
	if bi == nil {
		return nil, fmt.Errorf("%s identity not bound to any user", authType)
	}
	// 绑定必须指向一个具体用户：RPC 没有「租户级凭证」这种身份。
	// 鉴权后的 AuthInfo.ID 就是运行域、会话与审计归属的依据，允许为空会让
	// 「有会话但不记记忆」这类静默降级重新出现（见 agent 层 sessionMeta 的口径）。
	// 列定义是 uuid NOT NULL，这里是防御性硬校验：将来新增认证分支忘了带用户会当场 401。
	if strings.TrimSpace(bi.UserID) == "" {
		return nil, fmt.Errorf("%s identity is not bound to a user", authType)
	}
	if bi.Status != auth.UserStatusActive {
		return nil, fmt.Errorf("bound user is not active (status=%q)", bi.Status)
	}
	return bi, nil
}

// authInfoFromBinding 把绑定身份与它的端点访问范围装进 AuthInfo。
//
// 范围只做**收窄**：不限制时 TargetAllow 留空（TargetAllowed 恒真）；
// restrict=true 但白名单为空时，任何端点都会被拒 —— 这是「全禁」的表达方式，
// 不能退化成「不限制」。
func authInfoFromBinding(schema string, bi *svcauthbindings.BindingIdentity) *auth.AuthInfo {
	ai := &auth.AuthInfo{ID: bi.UserID, Schema: schema, RestrictTargets: bi.RestrictTargets,
		ExcludeSupervisor: bi.ExcludeSupervisor}
	if bi.RestrictTargets {
		ai.TargetAllow = make(map[string]struct{}, len(bi.TargetAllow))
		for _, k := range bi.TargetAllow {
			ai.TargetAllow[k] = struct{}{}
		}
	}
	return ai
}

// unmarshalAuthConfig 从 xsql.JSONB 解析出具体配置结构
func unmarshalAuthConfig(raw xsql.JSONB, v interface{}) error {
	data, ok := raw.Data.(json.RawMessage)
	if !ok {
		// 兜底：尝试从 []byte 转 RawMessage
		if b, ok := raw.Data.([]byte); ok {
			data = json.RawMessage(b)
		} else {
			return fmt.Errorf("auth_config is empty")
		}
	}
	if len(data) == 0 {
		return fmt.Errorf("auth_config is empty")
	}
	return json.Unmarshal(data, v)
}

// containsAudience 校验 audience 是否包含期望值
func containsAudience(aud interface{}, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []interface{}:
		for _, a := range v {
			if s, ok := a.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// containsScope 校验 scope 字符串（空格分隔）是否包含所需 scope
func containsScope(scope, want string) bool {
	for _, s := range strings.Fields(scope) {
		if s == want {
			return true
		}
	}
	return false
}

// jwksCache 简单的内存缓存（按 jwks_url），避免每次请求都拉取。
// 多实例部署下各实例各存一份是刻意的：数据来自外部 IdP、我们不可写、无需跨实例失效。
var (
	jwksCacheMu sync.RWMutex
	jwksCache   = map[string]*jwksSet{}
)

type jwksSet struct {
	Keys      []jwkEntry `json:"keys"`
	fetchedAt time.Time
}

type jwkEntry struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// jwksKeyFunc 返回一个 jwt.Keyfunc，从 JWKS 端点取公钥验签
func jwksKeyFunc(jwksURL string) (jwt.Keyfunc, error) {
	set, err := fetchJWKS(jwksURL)
	if err != nil {
		return nil, err
	}
	return func(token *jwt.Token) (interface{}, error) {
		kid, _ := token.Header["kid"].(string)
		for _, k := range set.Keys {
			if kid != "" && k.Kid != kid {
				continue
			}
			key, err := buildVerifyKey(k)
			if err != nil {
				continue
			}
			return key, nil
		}
		return nil, fmt.Errorf("no matching JWK found")
	}, nil
}

// fetchJWKS 拉取并缓存 JWKS。
// 命中走读锁；HTTP 拉取放在锁外（不阻塞其它请求）；写入走写锁。
// 未做 singleflight：过期瞬间多个并发请求可能各拉一次，属可接受的重复。
func fetchJWKS(jwksURL string) (*jwksSet, error) {
	jwksCacheMu.RLock()
	set, ok := jwksCache[jwksURL]
	jwksCacheMu.RUnlock()
	if ok && time.Since(set.fetchedAt) < 1*time.Hour {
		return set, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	var fetched jwksSet
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	fetched.fetchedAt = time.Now()

	jwksCacheMu.Lock()
	jwksCache[jwksURL] = &fetched
	jwksCacheMu.Unlock()
	return &fetched, nil
}

// buildVerifyKey 从 JWK 条目构造 Go 验签公钥
func buildVerifyKey(k jwkEntry) (interface{}, error) {
	switch k.Kty {
	case "RSA":
		pub, err := buildRSAPublicKey(k.N, k.E)
		if err != nil {
			return nil, err
		}
		return pub, nil
	case "EC":
		pub, err := buildECPublicKey(k.Crv, k.X, k.Y)
		if err != nil {
			return nil, err
		}
		return pub, nil
	default:
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	}
}

// writeWWWAuthenticate 在无凭证时写 401 + WWW-Authenticate 指引前端去 IdP 换 token
func writeWWWAuthenticate(w http.ResponseWriter, authInfo *auth.AuthInfo) {
	cfg, err := GetServerConfig(authInfo, "oauth")
	if err != nil {
		// oauth 未配置：给出通用提示
		w.Header().Set("WWW-Authenticate", `Bearer realm="flame-rpc"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var oc OAuthConfig
	if e := unmarshalAuthConfig(cfg.AuthConfig, &oc); e == nil {
		val := fmt.Sprintf(`Bearer realm="flame-rpc", authorization_uri="%s", scopes_supported="%s"`,
			oc.AuthorizeEndpoint, oc.RequiredScope)
		w.Header().Set("WWW-Authenticate", val)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer realm="flame-rpc"`)
	}
	w.WriteHeader(http.StatusUnauthorized)
}
