// Package listingpolicy is the review policy for tenant-listed assets (a global default plus per-tenant overrides).
//
// The shape matches capability_gates / plugin_gates: the default is "listing needs no review" (list directly), and the
// tenants / scenarios that do need review write a row explicitly. That is the opposite of the other gates' "refused by
// default", deliberately: a listing review is daily friction for a tenant, while the asset marketplace entrance has
// already been filtered by "the listing side lists on purpose and the installing side decides for itself".
// Two levels:
//
//	platform.listing_settings  the global default (a single row with id=1; no row = no review)
//	platform.listing_policies  per-tenant overrides (a row wins)
//
// In the database rather than config.yaml: this is a governance policy, and whoever changes it should be an admin with a
// platform account, not "whoever can log onto the server, edit a file and restart".
//
// It only affects "a tenant listing an asset" and not "pulling from a remote marketplace" — the latter follows
// peer_servers.install_policy.
package listingpolicy

import (
	"database/sql"
	"strings"

	"flame/nexus/shared/config"
	"flame/nexus/shared/db"

	"github.com/lib/pq"
)

func table(name string) string {
	return pq.QuoteIdentifier(config.Cfg.Database.PlatformSchema) + "." + name
}

// Default is the global default (no row = no review).
//
// Note the difference from "the read failed": no row is the normal initial state (no review), while a query error returns
// "review required" — when the policy table is broken, one extra manual confirmation is preferable to letting unreviewed
// content into the marketplace along with the failure.
func Default() (bool, error) {
	var v bool
	err := db.DB.QueryRow(`SELECT require_review FROM ` + table("listing_settings") + ` WHERE id = 1`).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return v, nil
}

// RequireReview reports whether this tenant's listings must be reviewed: an override wins, otherwise the global default.
//
// A failed read of the override row always returns "review required" (fail closed): when the policy table misbehaves, one
// extra manual confirmation is preferable to letting unreviewed content into the marketplace.
func RequireReview(schema string) (bool, error) {
	schema = strings.TrimSpace(schema)
	if schema != "" {
		var v bool
		err := db.DB.QueryRow(`SELECT require_review FROM `+table("listing_policies")+
			` WHERE schema_name = $1`, schema).Scan(&v)
		if err == nil {
			return v, nil
		}
		if err != sql.ErrNoRows {
			return true, err
		}
	}
	return Default()
}

// SetDefault changes the global default.
func SetDefault(require bool, by string) error {
	var byVal any
	if by = strings.TrimSpace(by); by != "" {
		byVal = by
	}
	_, err := db.DB.Exec(`INSERT INTO `+table("listing_settings")+
		` (id, require_review, updated_at, updated_by) VALUES (1, $1, now(), $2::uuid)
		 ON CONFLICT (id) DO UPDATE SET require_review = EXCLUDED.require_review,
		     updated_at = now(), updated_by = EXCLUDED.updated_by`, require, byVal)
	return err
}

// SetOverride sets or clears a tenant's override: a nil require deletes the row (back to the global default).
//
// "Whether there is a row" expresses "follow the default or not", rather than storing a tri-state string: a tri-state
// column eventually produces a self-contradictory row of "the value is inherit while another column is still set".
func SetOverride(schema string, require *bool, by string) error {
	if require == nil {
		_, err := db.DB.Exec(`DELETE FROM `+table("listing_policies")+` WHERE schema_name = $1`, schema)
		return err
	}
	var byVal any
	if by = strings.TrimSpace(by); by != "" {
		byVal = by
	}
	_, err := db.DB.Exec(`INSERT INTO `+table("listing_policies")+
		` (schema_name, require_review, updated_at, updated_by) VALUES ($1, $2, now(), $3::uuid)
		 ON CONFLICT (schema_name) DO UPDATE SET require_review = EXCLUDED.require_review,
		     updated_at = now(), updated_by = EXCLUDED.updated_by`, schema, *require, byVal)
	return err
}

// ListOverrides lists the tenants that have an override (schema_name → whether review is required).
func ListOverrides() (map[string]bool, error) {
	rows, err := db.DB.Query(`SELECT schema_name, require_review FROM ` + table("listing_policies"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			name string
			v    bool
		)
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, rows.Err()
}
