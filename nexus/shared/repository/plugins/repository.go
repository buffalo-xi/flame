// Package plugins stores the tenant-level plugin configuration (table t0.plugins).
//
// It has the same shape as the mcpconnections repository, but is narrower: **the plugin itself is not in the database** —
// the tool surface and the implementation come from disk ({root_dir}/plugins), and this only records "which plugin this
// tenant enabled, with which implementation, configured how".
//
// A row is identified by (kind, name) rather than name alone: plugins of different kinds are separate namespaces, so
// sources and tools of the same name must not overwrite each other later. Nearly every caller already holds a manifest,
// so take kind straight from manifest.Kind instead of writing a bare string here.
package plugins

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

const (
	// KindTools is the tool plugin. Currently the only supported plugin type (same value as service/plugin.KindTools and
	// pluginpolicy.KindTools; the three live in different layers, so each declares its own instead of importing the
	// others).
	KindTools = "tools"

	// tableName is the tenant-level plugin configuration table. Kept in one place so a future change touches only here.
	// The structure is in nexus/sql/plugins.sql and platform/provision/tenant_template.sql.
	tableName = "plugins"
)

// Repository is the plugin configuration repository.
type Repository interface {
	// Get fetches by (kind, name); missing returns sql.ErrNoRows.
	Get(authInfo *auth.AuthInfo, kind, name string) (*models.Plugin, error)

	// List lists every plugin configured for this tenant (disabled ones and all kinds included).
	// Not splitting by kind is deliberate: the admin side and the platform matrix both want it all at once, and a query
	// per kind would turn into N SQL statements.
	List(authInfo *auth.AuthInfo) ([]models.Plugin, error)

	// Upsert inserts or updates by (kind, name) (overwriting impl / fallbacks / configs / is_active).
	// An empty p.Kind is an outright error, **never** a fallback to a default — a silent default would write the row
	// somewhere nothing reads.
	Upsert(authInfo *auth.AuthInfo, p *models.Plugin) error

	// SetActive changes the enabled state only; missing returns sql.ErrNoRows.
	SetActive(authInfo *auth.AuthInfo, kind, name string, active bool) error

	// Delete removes this tenant's plugin configuration (the plugin on disk is unaffected).
	Delete(authInfo *auth.AuthInfo, kind, name string) error

	// TouchUsed records one successful call (and clears the previous error). Silently succeeds when the row is gone.
	TouchUsed(authInfo *auth.AuthInfo, kind, name string) error

	// SetError records a one-line reason for a failure (the implementation truncates it). Silently succeeds when the row
	// is gone.
	SetError(authInfo *auth.AuthInfo, kind, name, message string) error
}
