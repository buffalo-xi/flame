// Package svcpath consolidates the "service endpoint path ↔ tenant schema" convention (shared by MCP and RPC).
//
// The endpoint shape is locked to "the endpoint segment is followed by exactly one schema segment, which is the last":
//
//	POST /service/mcp/{schema}
//	POST /service/rpc/{schema}
//
// That shape is guaranteed by the gin routes /mcp/:schema (:schema does not cross "/", and extra segments 404 outright).
// This package provides two things:
//  1. passing the route parameter -> request context -> service handler (WithSchema / FromContext);
//  2. parsing the tenant schema out of an outbound connection URL (SchemaFromURL), used by the "self-connection" check.
package svcpath

import (
	"context"
	"net/url"
	"strings"
)

// tenantSchemaKey is a private type for the context key, so it cannot collide with another package's keys.
type tenantSchemaKey struct{}

// WithSchema puts the tenant schema parsed from the path into the request context.
func WithSchema(ctx context.Context, schema string) context.Context {
	return context.WithValue(ctx, tenantSchemaKey{}, schema)
}

// FromContext takes the tenant schema carried by the path; an unset one returns an empty string.
func FromContext(ctx context.Context) string {
	s, _ := ctx.Value(tenantSchemaKey{}).(string)
	return s
}

// SchemaFromURL parses the tenant schema out of a URL shaped "…/{endpoint}/{schema}".
// The shape is locked: the endpoint segment must be followed by exactly one segment and that segment must be last,
// otherwise an empty string is returned.
// endpoint is the endpoint name ("mcp" / "rpc"), case insensitive.
func SchemaFromURL(rawURL, endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, s := range segs {
		if !strings.EqualFold(s, endpoint) {
			continue
		}
		// Locked shape: exactly one segment after endpoint, and that segment is last
		if i+1 == len(segs)-1 {
			return segs[i+1]
		}
		return ""
	}
	return ""
}
