package server

import (
	"flame/tools/xsql"

	"github.com/google/uuid"
)

// McpServerConfig 对应每租户 schema 内的 mcp_server_configs 表
// DDL（已建好）：
//
//	CREATE TABLE <schema>.mcp_server_configs (
//	    id uuid NOT NULL, -- 无默认值：由程序生成（见 NewID）
//	    auth_type varchar(20) NOT NULL,
//	    auth_config jsonb DEFAULT '{}'::jsonb NULL,
//	    is_active bool DEFAULT true NULL,
//	    created_at timestamptz DEFAULT now() NULL,
//	    updated_at timestamptz DEFAULT now() NULL,
//	    CONSTRAINT mcp_server_configs_check CHECK (auth_type IN ('apikey','oauth')),
//	    CONSTRAINT mcp_server_configs_pk PRIMARY KEY (id),
//	    CONSTRAINT mcp_server_configs_unique UNIQUE (auth_type)
//	);
type McpServerConfig struct {
	ID         string     `json:"id"`
	AuthType   string     `json:"auth_type"` // 'apikey' | 'oauth'
	AuthConfig xsql.JSONB `json:"auth_config"`
	IsActive   bool       `json:"is_active"`
	CreatedAt  xsql.Time  `json:"created_at"`
	UpdatedAt  xsql.Time  `json:"updated_at"`
}

// NewID 生成新的 uuid（表内 id 由程序提供，无数据库默认值）
func (McpServerConfig) NewID() string {
	return uuid.NewString()
}

// OAuthConfig oauth 行的 auth_config 结构（后端只验不发放 token）
type OAuthConfig struct {
	Issuer            string `json:"issuer"`             // IdP issuer
	AuthorizeEndpoint string `json:"authorize_endpoint"` // 401 时 WWW-Authenticate 使用
	JWKSURL           string `json:"jwks_url"`           // JWKS 公钥地址
	Audience          string `json:"audience"`           // 期望的 token audience
	RequiredScope     string `json:"required_scope"`     // 期望的 scope
}
