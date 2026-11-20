package svcauthbindings

import (
	"database/sql"
	"fmt"
	"strings"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"

	"github.com/google/uuid"
)

// PostgresRepository is the PostgreSQL implementation of service-side authentication binding storage.
type PostgresRepository struct{}

// ResolveBindingIdentity resolves a binding and carries the account status out (LEFT JOIN, so a missing user row is
// allowed). Whether to let the call through is the caller's decision (see authorizeBinding in
// service/{mcp,rpc}/server/auth.go).
func (r *PostgresRepository) ResolveBindingIdentity(schema, authType, issuer, subject string) (*BindingIdentity, error) {
	var bi BindingIdentity
	var bindingID string
	err := db.DB.QueryRow(
		fmt.Sprintf(`SELECT b.id, b.user_id, COALESCE(u.status, ''), b.restrict_targets, b.exclude_supervisor
			FROM %s.svc_auth_bindings b
			LEFT JOIN %s.users u ON u.id = b.user_id
			WHERE b.auth_type = $1 AND b.external_issuer = $2 AND b.external_subject = $3
			  AND b.is_active = true`, schema, schema),
		authType, issuer, subject,
	).Scan(&bindingID, &bi.UserID, &bi.Status, &bi.RestrictTargets, &bi.ExcludeSupervisor)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	// The allowlist is queried only when the restriction is really on: it saves a round trip when it is off, and it lets
	// existing tenants that never ran the migration script keep authenticating (no restriction, no touching the new
	// column / table).
	if bi.RestrictTargets {
		rows, qerr := db.DB.Query(
			fmt.Sprintf(`SELECT target_type, target_id FROM %s.svc_auth_binding_targets WHERE binding_id = $1`, schema),
			bindingID,
		)
		if qerr != nil {
			// fail closed: if the policy cannot be read, do not let the call through (the caller answers 401); never fall
			// back to "unrestricted"
			return nil, fmt.Errorf("load allowed targets: %w", qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var tt, tid string
			if serr := rows.Scan(&tt, &tid); serr != nil {
				return nil, serr
			}
			bi.TargetAllow = append(bi.TargetAllow, tt+":"+tid)
		}
		if rerr := rows.Err(); rerr != nil {
			return nil, rerr
		}
	}
	return &bi, nil
}

// List lists bindings (filtered by type when authType is non-empty).
func (r *PostgresRepository) List(schema, authType string) ([]models.SvcAuthBinding, error) {
	where := ""
	args := []interface{}{}
	if authType != "" {
		where = " WHERE b.auth_type = $1"
		args = append(args, authType)
	}

	// target_count comes from a subquery: there are very few bindings, and it saves the frontend from pulling the
	// allowlist again for every card.
	query := fmt.Sprintf(`SELECT b.id, b.auth_type, b.external_issuer, b.external_subject, b.user_id,
			b.is_active, b.restrict_targets, b.exclude_supervisor,
			(SELECT count(*) FROM %s.svc_auth_binding_targets t WHERE t.binding_id = b.id) AS target_count,
			b.created_at, b.updated_at
		FROM %s.svc_auth_bindings b%s ORDER BY b.created_at DESC`, schema, schema, where)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]models.SvcAuthBinding, 0)
	for rows.Next() {
		var b models.SvcAuthBinding
		if err := rows.Scan(&b.ID, &b.AuthType, &b.ExternalIssuer, &b.ExternalSubject, &b.UserID,
			&b.IsActive, &b.RestrictTargets, &b.ExcludeSupervisor, &b.TargetCount, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, rows.Err()
}

// GetByID fetches a binding by id; missing returns (nil, nil).
func (r *PostgresRepository) GetByID(schema, id string) (*models.SvcAuthBinding, error) {
	var b models.SvcAuthBinding
	err := db.DB.QueryRow(`SELECT id, auth_type, external_issuer, external_subject, user_id, is_active,
			restrict_targets, exclude_supervisor,
			(SELECT count(*) FROM `+schema+`.svc_auth_binding_targets t WHERE t.binding_id = `+schema+`.svc_auth_bindings.id),
			created_at, updated_at
		FROM `+schema+`.svc_auth_bindings WHERE id = $1`, id).
		Scan(&b.ID, &b.AuthType, &b.ExternalIssuer, &b.ExternalSubject, &b.UserID, &b.IsActive,
			&b.RestrictTargets, &b.ExcludeSupervisor, &b.TargetCount, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// Create creates a binding (the id is generated when empty).
func (r *PostgresRepository) Create(schema string, b *models.SvcAuthBinding) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	// restrict_targets is deliberately not written: the column default (true) decides, so a new binding is restricted
	// with an empty allowlist = not a single endpoint is callable.
	// Writing b.RestrictTargets explicitly here would turn "left unset" into "wide open" through the bool zero value, which
	// is exactly the safe default being broken.
	_, err := db.DB.Exec(`INSERT INTO `+schema+`.svc_auth_bindings
		(id, auth_type, external_issuer, external_subject, user_id, is_active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6, NOW(), NOW())`,
		b.ID, b.AuthType, b.ExternalIssuer, b.ExternalSubject, b.UserID, b.IsActive)
	return err
}

// Update updates a binding (external_subject / user_id / is_active).
func (r *PostgresRepository) Update(schema string, b *models.SvcAuthBinding) error {
	res, err := db.DB.Exec(`UPDATE `+schema+`.svc_auth_bindings
		SET external_subject = $2, user_id = $3, is_active = $4, updated_at = NOW()
		WHERE id = $1`,
		b.ID, b.ExternalSubject, b.UserID, b.IsActive)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete deletes a binding.
func (r *PostgresRepository) Delete(schema, id string) error {
	_, err := db.DB.Exec(`DELETE FROM `+schema+`.svc_auth_bindings WHERE id = $1`, id)
	return err
}

// BindingTarget is one endpoint target (agent / flow) in a credential's allowlist.
//
// The grant is per **target** rather than per endpoint: one agent usually has both an rpc and an mcp endpoint, and
// granting per endpoint would mean "only rpc was ticked, so the MCP connection is empty" and "after an endpoint is
// retracted and reopened, the grant is void".
type BindingTarget struct {
	TargetType string   `json:"target_type"`
	TargetID   string   `json:"target_id"`
	Name       string   `json:"name,omitempty"`
	Protocols  []string `json:"protocols,omitempty"` // the protocols this target has exposed (for display)
	// IsSupervisor is meaningful only in the candidate list: whether the target is the super agent.
	// The UI groups it on top and warns accordingly — granting the supervisor endpoint is opening the tenant's management
	// capability to the outside.
	IsSupervisor bool `json:"is_supervisor,omitempty"`
}

// GetTargets reads a binding's endpoint access scope (the restrict switch + exclude-supervisor + the allowlist).
func (r *PostgresRepository) GetTargets(schema, bindingID string) (bool, bool, []BindingTarget, error) {
	var restrict, excludeSupervisor bool
	if err := db.DB.QueryRow(`SELECT restrict_targets, exclude_supervisor
		FROM `+schema+`.svc_auth_bindings WHERE id = $1`, bindingID).
		Scan(&restrict, &excludeSupervisor); err != nil {
		if err == sql.ErrNoRows {
			return false, false, nil, sql.ErrNoRows
		}
		return false, false, nil, err
	}

	rows, err := db.DB.Query(`SELECT target_type, target_id
		FROM `+schema+`.svc_auth_binding_targets WHERE binding_id = $1
		ORDER BY target_type, target_id`, bindingID)
	if err != nil {
		return false, false, nil, err
	}
	defer rows.Close()

	out := make([]BindingTarget, 0, 4)
	for rows.Next() {
		var t BindingTarget
		if serr := rows.Scan(&t.TargetType, &t.TargetID); serr != nil {
			return false, false, nil, serr
		}
		out = append(out, t)
	}
	return restrict, excludeSupervisor, out, rows.Err()
}

// SetTargets replaces a binding's endpoint access scope wholesale (transaction: flip the switch, delete the old rows,
// insert the new ones).
//
// When restrict=true, exclude_supervisor is written as false: the two settings are mutually exclusive, the allowlist
// already states the scope precisely, and leaving a true behind would only make the next dialog confusing about which
// setting is in force.
func (r *PostgresRepository) SetTargets(schema, bindingID string, restrict, excludeSupervisor bool, targets []BindingTarget) error {
	if restrict {
		excludeSupervisor = false
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	res, err := tx.Exec(`UPDATE `+schema+`.svc_auth_bindings
		SET restrict_targets = $1, exclude_supervisor = $2, updated_at = NOW() WHERE id = $3`,
		restrict, excludeSupervisor, bindingID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}

	if _, err = tx.Exec(`DELETE FROM `+schema+`.svc_auth_binding_targets WHERE binding_id = $1`, bindingID); err != nil {
		return err
	}
	for _, t := range targets {
		if _, err = tx.Exec(`INSERT INTO `+schema+`.svc_auth_binding_targets (binding_id, target_type, target_id)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, bindingID, t.TargetType, t.TargetID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTargetCandidates lists the grantable candidate targets: agents / flows that **have an exposed endpoint**.
//
// It only filters out targets with no exposed endpoint — a target without one cannot be called even when granted, and
// listing it would only make people believe it was configured.
//
// The super agent (is_supervisor) is not excluded but returned flagged: its endpoint is the tenant's management entrance,
// so granting it opens "create / change / delete an agent, change grants" to an external key. It is therefore **visible
// but requiring an explicit tick** (the UI puts it on top as its own group); it must not be swept up by a select-all the
// way ordinary endpoints are.
func (r *PostgresRepository) ListTargetCandidates(schema string) ([]BindingTarget, error) {
	rows, err := db.DB.Query(fmt.Sprintf(`SELECT e.target_type, e.target_id,
			COALESCE(a.name, f.name, '') AS name,
			COALESCE(bool_or(COALESCE(a.is_supervisor, false)), false) AS is_supervisor,
			COALESCE(string_agg(DISTINCT e.protocol, ',' ORDER BY e.protocol), '') AS protocols
		FROM %s.svc_endpoints e
		LEFT JOIN %s.agents a ON e.target_type = 'agent' AND a.id = e.target_id
		LEFT JOIN %s.flows f ON e.target_type = 'flow' AND f.id = e.target_id
		WHERE e.exposed = true AND e.target_id IS NOT NULL
		GROUP BY e.target_type, e.target_id, a.name, f.name
		ORDER BY name`, schema, schema, schema))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]BindingTarget, 0, 8)
	for rows.Next() {
		var t BindingTarget
		var protocols string
		if serr := rows.Scan(&t.TargetType, &t.TargetID, &t.Name, &t.IsSupervisor, &protocols); serr != nil {
			return nil, serr
		}
		if protocols != "" {
			t.Protocols = strings.Split(protocols, ",")
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
