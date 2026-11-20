package server

import (
	"context"
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

// contextKey carries the tenant information from the outer handler to the getServer callback.
type contextKey string

const authInfoKey contextKey = "mcp_auth_info"

// AuthInfoFromContext pulls the authenticated tenant out of the request context.
func AuthInfoFromContext(ctx context.Context) (*auth.AuthInfo, bool) {
	a, ok := ctx.Value(authInfoKey).(*auth.AuthInfo)
	return a, ok
}

// errNoAuth means the request carried no credential at all (answer with 401 + WWW-Authenticate).
var errNoAuth = fmt.Errorf("no authorization provided")

// resolveTenant takes the tenant identifier (that is, the Postgres schema name) from the request path.
// Path shape: POST /service/mcp/{schema}, injected into the context by the /mcp/:schema route (see svcpath).
func resolveTenant(r *http.Request) (*auth.AuthInfo, error) {
	schema := strings.TrimSpace(svcpath.FromContext(r.Context()))
	if schema == "" {
		return nil, fmt.Errorf("missing tenant schema in path: /service/mcp/<schema>")
	}
	// First gate: the schema name may only contain letters, digits and underscores, to rule out SQL injection.
	if !isValidSchemaName(schema) {
		return nil, fmt.Errorf("invalid tenant schema name %q", schema)
	}
	// Second gate: the registry allowlist — only tenants that are registered and active are accepted
	// (deactivating a tenant takes effect immediately). A failed lookup always rejects (fail-closed);
	// never treat it as "allowed" or as "not registered" and continue silently.
	active, terr := sharedtenants.IsActive(schema)
	if terr != nil {
		return nil, fmt.Errorf("tenant lookup failed: %w", terr)
	}
	if !active {
		return nil, fmt.Errorf("tenant %q not found or inactive", schema)
	}
	return &auth.AuthInfo{Schema: schema}, nil
}

// isValidSchemaName allowlist-checks the tenant identifier (it must be a legal identifier, so that
// concatenating it into SQL cannot inject).
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

// authenticate validates the request credential and returns an AuthInfo carrying the user identity.
// Without a credential it returns errNoAuth (the caller should answer 401 + WWW-Authenticate).
func authenticate(authInfo *auth.AuthInfo, r *http.Request) (*auth.AuthInfo, error) {
	bearer := extractBearer(r)
	if bearer == "" {
		return nil, errNoAuth
	}

	// Dispatch on the value's shape: a JWT (starting with eyJ, three segments) goes to oauth, everything else to apikey.
	if looksLikeJWT(bearer) {
		return verifyOAuth(authInfo, bearer)
	}
	return verifyAPIKey(authInfo, bearer)
}

// extractBearer pulls the token out of "Authorization: Bearer <token>".
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// looksLikeJWT heuristically decides whether the token is a JWT (starts with eyJ, contains two dots).
func looksLikeJWT(token string) bool {
	if !strings.HasPrefix(token, "eyJ") {
		return false
	}
	dots := strings.Count(token, ".")
	return dots == 2
}

// verifyAPIKey validates an apikey: the scheme must be enabled and a binding must match in
// svc_auth_bindings, whose user is returned.
func verifyAPIKey(authInfo *auth.AuthInfo, token string) (*auth.AuthInfo, error) {
	cfg, err := GetServerConfig(authInfo, "apikey")
	if err != nil {
		return nil, fmt.Errorf("apikey auth not configured: %w", err)
	}
	if !cfg.IsActive {
		return nil, fmt.Errorf("apikey auth disabled")
	}

	// The plaintext token is looked up in the binding table directly (external_issuer is empty for apikey).
	bi, err := authorizeBinding(authInfo.Schema, "apikey", "", token)
	if err != nil {
		return nil, err
	}
	return authInfoFromBinding(authInfo.Schema, bi), nil
}

// verifyOAuth verifies a JWT against JWKS, checks audience / scope, and resolves the bound owner in
// svc_auth_bindings.
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

	// Audience check.
	if oc.Audience != "" {
		if aud, ok := claims["aud"]; ok {
			if !containsAudience(aud, oc.Audience) {
				return nil, fmt.Errorf("oauth token audience mismatch")
			}
		} else {
			return nil, fmt.Errorf("oauth token missing audience")
		}
	}

	// Scope check (required_scope must appear in the token scope).
	if oc.RequiredScope != "" {
		scp, _ := claims["scope"].(string)
		if !containsScope(scp, oc.RequiredScope) {
			return nil, fmt.Errorf("oauth token missing required scope %q", oc.RequiredScope)
		}
	}

	// Extract the external identity from the token (iss + sub) and look it up in svc_auth_bindings.
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

// authorizeBinding resolves the binding and performs exactly one check: the bound account is active.
// It deliberately ignores roles — a binding only gives a service call an identity (for auditing), it
// does not grant permissions; deactivating or locking an account invalidates its bindings immediately
// (no need to delete them) and restoring the account brings them back.
//
// The return value also carries the credential's endpoint scope (restrict_targets / allowlist), which
// the caller puts into the AuthInfo.
func authorizeBinding(schema, authType, issuer, subject string) (*svcauthbindings.BindingIdentity, error) {
	bi, err := svcauthbindings.NewRepository().ResolveBindingIdentity(schema, authType, issuer, subject)
	if err != nil {
		return nil, fmt.Errorf("resolve %s binding: %w", authType, err)
	}
	if bi == nil {
		return nil, fmt.Errorf("%s identity not bound to any user", authType)
	}
	// A binding must point at a concrete user: MCP has no such identity as a "tenant-level credential".
	// The authenticated AuthInfo.ID is what the run domain, sessions and audit attribution rely on, and
	// letting it be empty would bring back silent downgrades such as "a session but no memory"
	// (see sessionMeta in the agent layer). The column is uuid NOT NULL, so this is a defensive hard
	// check: a future auth branch that forgets the user fails with a 401 right here.
	if strings.TrimSpace(bi.UserID) == "" {
		return nil, fmt.Errorf("%s identity is not bound to a user", authType)
	}
	if bi.Status != auth.UserStatusActive {
		return nil, fmt.Errorf("bound user is not active (status=%q)", bi.Status)
	}
	return bi, nil
}

// authInfoFromBinding packs the bound identity and its endpoint scope into an AuthInfo.
//
// The scope can only **narrow**: when unrestricted, TargetAllow stays empty (TargetAllowed is always
// true); when restrict=true but the allowlist is empty, every endpoint is rejected — that is how "deny
// everything" is expressed, and it must never degrade into "unrestricted".
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

// unmarshalAuthConfig decodes a concrete config structure from xsql.JSONB.
func unmarshalAuthConfig(raw xsql.JSONB, v interface{}) error {
	data, ok := raw.Data.(json.RawMessage)
	if !ok {
		// Fallback: try converting a []byte to RawMessage.
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

// containsAudience checks whether the audience contains the expected value.
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

// containsScope checks whether the scope string (space separated) contains the required scope.
func containsScope(scope, want string) bool {
	for _, s := range strings.Fields(scope) {
		if s == want {
			return true
		}
	}
	return false
}

// jwksCache is a simple in-memory cache (keyed by jwks_url) so that requests do not fetch it every time.
// Each instance keeping its own copy in a multi-instance deployment is deliberate: the data comes from an
// external IdP, we cannot write it, and it needs no cross-instance invalidation.
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

// jwksKeyFunc returns a jwt.Keyfunc that verifies signatures with a public key taken from the JWKS endpoint.
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

// fetchJWKS fetches and caches the JWKS.
// A cache hit takes the read lock; the HTTP fetch happens outside the lock (so it does not block other
// requests); storing takes the write lock. There is no singleflight: several concurrent requests may each
// fetch once right after expiry, which is acceptable duplication.
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

// buildVerifyKey builds a Go verification public key from a JWK entry.
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

// writeWWWAuthenticate writes 401 + WWW-Authenticate when no credential was supplied, telling the client
// where to obtain a token from the IdP.
func writeWWWAuthenticate(w http.ResponseWriter, authInfo *auth.AuthInfo) {
	cfg, err := GetServerConfig(authInfo, "oauth")
	if err != nil {
		// oauth is not configured: give a generic hint.
		w.Header().Set("WWW-Authenticate", `Bearer realm="flame-mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var oc OAuthConfig
	if e := unmarshalAuthConfig(cfg.AuthConfig, &oc); e == nil {
		val := fmt.Sprintf(`Bearer realm="flame-mcp", authorization_uri="%s", scopes_supported="%s"`,
			oc.AuthorizeEndpoint, oc.RequiredScope)
		w.Header().Set("WWW-Authenticate", val)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer realm="flame-mcp"`)
	}
	w.WriteHeader(http.StatusUnauthorized)
}
