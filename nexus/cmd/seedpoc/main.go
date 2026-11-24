// Command seedpoc prepares a fresh installation for the single-container demo (deploy/poc):
//
//	plat      platform administrator (the account the console is entered with)
//	user01    a platform "user" that owns the demo tenant
//	t0        the demo tenant: template tables, the preinstalled `main` agent, the four protected skills, the seeded
//	          LLM provider / model (keyless DeepSeek + deepseek-flash), and its own first administrator (the primary user)
//	          every platform capability switched on (exec / shell / code / browser / egress)
//	          an apikey credential bound to that primary user, usable against the tenant's MCP and RPC endpoints
//
// Why a separate command instead of "nexus creates it at startup": nexus deliberately creates no account and no tenant on
// startup (the console is the only way in). A demo box needs an environment that is already populated, and reusing
// platform/provision means the tenant is created exactly the way the API creates one — same template, same definition
// files, same registry row.
//
// Idempotent: every step checks first, so running it on an already-seeded database changes only what is missing. It is
// safe to run before nexus starts (the caches provision invalidates live in the calling process, and nexus is not up
// yet).
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"

	"flame/nexus/platform/provision"
	"flame/nexus/shared/account"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/config"
	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/tools/xlog"

	"github.com/google/uuid"
)

// capabilities are the platform-level switches (platform.tenant_capabilities). "Everything open" for a demo box means all
// of them: exec / shell / code (the three sandbox process shapes), browser, and the network egress those commands need.
//
// The list comes from shared/capability rather than being spelled out here: it is the same list the tenant provisioning
// path grants and the same one the admin console renders, so a capability added later cannot be silently left off the
// demo box.
var capabilities = capability.All()

func main() {
	var (
		cfgPath   = flag.String("config", "/etc/flame/nexus/config.yaml", "path to the nexus configuration file")
		schema    = flag.String("schema", "t0", "tenant schema to create")
		platName  = flag.String("plat", "plat", "platform administrator login name")
		ownerName = flag.String("owner", "user01", "platform user that owns the tenant")
		password  = flag.String("password", "12345678", "password for the platform accounts and the tenant administrator")
		title     = flag.String("title", "Demo tenant", "tenant display name")
		apiKey    = flag.String("apikey", "sk-poc-t0-primary", "apikey granted to the tenant's primary user")
	)
	flag.Parse()

	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	config.Cfg = cfg

	// The logger is best-effort: the seeder's own output goes to stdout, and a read-only log directory must not stop the
	// seeding (it runs once, inside the container's bootstrap).
	if err := xlog.Init("/var/log/flame", "seedpoc"); err != nil {
		fmt.Fprintf(os.Stderr, "seedpoc: logger init failed (continuing): %v\n", err)
	}
	defer xlog.Close()

	if err := db.InitDatabase(); err != nil {
		fatal("connect database: %v", err)
	}
	defer db.DB.Close()

	platID := ensurePlatformUser(*platName, "platform administrator", "admin", *password)
	ownerID := ensurePlatformUser(*ownerName, *ownerName, "user", *password)

	created := ensureTenant(*schema, *title, ownerID, platID, *password)

	if err := verifySystemAssets(*schema); err != nil {
		fatal("check the preinstalled assets of %s: %v", *schema, err)
	}

	if err := ensureLLMSeed(*schema); err != nil {
		fatal("seed the llm configuration of %s: %v", *schema, err)
	}

	for _, capability := range capabilities {
		if err := allowCapability(*schema, capability); err != nil {
			fatal("allow capability %s: %v", capability, err)
		}
	}

	primaryID, err := tenantUserID(*schema, provision.DefaultAdminName)
	if err != nil {
		fatal("look up the tenant's primary user: %v", err)
	}
	if err := ensureAPICredential(*schema, primaryID, *apiKey); err != nil {
		fatal("create the api credential: %v", err)
	}

	fmt.Println("seedpoc: ready")
	fmt.Printf("  platform administrator : %s\n", *platName)
	fmt.Printf("  platform user (owner)  : %s\n", *ownerName)
	fmt.Printf("  tenant                 : %s (%s, freshly created: %t)\n", *schema, *title, created)
	fmt.Printf("  tenant administrator   : %s\n", provision.DefaultAdminName)
	fmt.Printf("  preinstalled agents    : %v\n", models.ProtectedAgentNames())
	fmt.Printf("  preinstalled skills    : %v\n", models.ProtectedSkillNames())
	fmt.Printf("  capabilities           : %v\n", capabilities)
	fmt.Printf("  api key (primary user) : %s\n", *apiKey)
	// Deliberately not "the endpoints are usable": authentication is in place, but the tenant has no exposed endpoint
	// yet, so tools/list is empty until someone exposes one (svc_endpoints.exposed). Saying more here would be the
	// kind of promise the first caller has to disprove.
	fmt.Println("  apikey auth ready      : /service/mcp/{schema} and /service/rpc/{schema}, Authorization: Bearer <key> (no endpoint exposed yet)")
}

// ensurePlatformUser returns the id of platform.users.name, creating the row when it is missing.
//
// The password is hashed here rather than seeded as a literal hash in SQL: the seeder runs in the same module as
// account.HashPassword, so the stored value is by construction the format the login path verifies.
func ensurePlatformUser(name, displayName, role, password string) string {
	var id string
	err := db.DB.QueryRow(`SELECT id::text FROM platform.users WHERE name = $1`, name).Scan(&id)
	switch {
	case err == nil:
		return id
	case !errors.Is(err, sql.ErrNoRows):
		fatal("look up platform user %s: %v", name, err)
	}

	hash, err := account.HashPassword(password)
	if err != nil {
		fatal("hash the password of %s: %v", name, err)
	}
	id = uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO platform.users
		(id, name, display_name, password, role, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', NOW(), NOW())`,
		id, name, displayName, hash, role); err != nil {
		fatal("create platform user %s: %v", name, err)
	}
	fmt.Printf("seedpoc: created platform user %s (%s)\n", name, role)
	return id
}

// ensureTenant creates the tenant through provision.Provision (the API path) and makes sure the registry row points at
// the owner. It reports whether the tenant was created by this run.
func ensureTenant(schema, title, ownerID, platID, password string) bool {
	var exists bool
	if err := db.DB.QueryRow(`SELECT EXISTS (SELECT 1 FROM platform.tenants WHERE schema_name = $1)`, schema).Scan(&exists); err != nil {
		fatal("look up tenant %s: %v", schema, err)
	}
	if exists {
		// An existing tenant keeps its data; only a missing owner is filled in.
		if _, err := db.DB.Exec(`UPDATE platform.tenants SET owner_user_id = $2, updated_at = NOW()
			WHERE schema_name = $1 AND owner_user_id IS NULL`, schema, ownerID); err != nil {
			fatal("assign the owner of tenant %s: %v", schema, err)
		}
		return false
	}

	if _, err := provision.Provision(provision.Request{
		Schema:        schema,
		DisplayName:   title,
		OwnerUserID:   ownerID,
		AdminName:     provision.DefaultAdminName,
		AdminPassword: password,
		CreatedBy:     platID,
	}); err != nil {
		fatal("create tenant %s: %v", schema, err)
	}
	fmt.Printf("seedpoc: created tenant %s (%s)\n", schema, title)
	return true
}

// verifySystemAssets fails when the tenant is missing something provisioning has materialised.
//
// The demo box is only usable because two kinds of preinstalled asset are there: the `main` supervisor (the console's
// management entry, and the agent that can create everything else) and the four protected skills (the knowledge source
// every meta capability reads before it writes anything). The names come from shared/models — the same list the delete /
// rename protection uses — so "protected but not packaged" cannot hide behind a second literal here.
//
// An existing tenant is verified too: this runs on every boot, so a hand-damaged tenant is reported instead of being
// handed to someone who then finds a console with nothing in it.
func verifySystemAssets(schema string) error {
	for _, name := range models.ProtectedAgentNames() {
		var supervisor bool
		err := db.DB.QueryRow(`SELECT is_supervisor FROM `+schema+`.agents WHERE name = $1`, name).Scan(&supervisor)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("agent %q is missing", name)
		case err != nil:
			return err
		case !supervisor:
			// CanDemoteSupervisor forbids demoting it, so false here means a broken tenant, not a configuration choice.
			return fmt.Errorf("agent %q no longer holds the supervisor privilege", name)
		}
	}

	for _, name := range models.ProtectedSkillNames() {
		var exists bool
		if err := db.DB.QueryRow(`SELECT EXISTS (SELECT 1 FROM `+schema+`.skills WHERE name = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("skill %q is missing", name)
		}
	}
	return nil
}

// The fixed identifiers the tenant template seeds (platform/provision/template/seed.sql); keep the two in step.
const (
	llmSeedProviderID = "a0000000-0000-4000-8000-000000000021"
	llmSeedModelID    = "a0000000-0000-4000-8000-000000000022"
)

// ensureLLMSeed fills in the seeded LLM configuration for a tenant that predates it: a keyless, disabled DeepSeek
// provider plus its model. A tenant created by this box gets the pair from the template; an existing t0 does not,
// because provisioning never re-runs - and without it the LLM page looks empty, which reads as a broken install.
//
// enabled=false on purpose: without a key the provider cannot serve a call, and an enabled provider is read-only, so a
// keyless seed must not block the very first edit.
//
// ON CONFLICT DO NOTHING on both statements: a provider or model the operator already configured is never touched.
// The model is attached by provider name rather than by the seeded id, so it still lands under the right provider
// when the seeded provider was skipped because a same-named row already existed.
func ensureLLMSeed(schema string) error {
	res, err := db.DB.Exec(`INSERT INTO `+schema+`.llm_providers
		(id, name, url, protocol, api_key, icon, note, enabled, extra_config)
		VALUES ($1, 'DeepSeek', 'https://api.deepseek.com', 'openai',
		        '', '/static/llm/compatible.png', '', false, '{}'::jsonb)
		ON CONFLICT DO NOTHING`, llmSeedProviderID)
	if err != nil {
		return fmt.Errorf("seed the llm provider: %w", err)
	}
	seeded, _ := res.RowsAffected()

	res, err = db.DB.Exec(`INSERT INTO `+schema+`.llm_models
		(id, name, provider_id, gateway_model, is_default, context_window, extra_params, capabilities, base_params)
		SELECT $1, 'deepseek-flash', p.id, 'deepseek-flash', false, 120000,
		       '{"thinking": {"type": "disabled"}}'::jsonb, '{}'::jsonb, '{}'::jsonb
		  FROM `+schema+`.llm_providers p
		 WHERE p.name = 'DeepSeek'
		 LIMIT 1
		ON CONFLICT DO NOTHING`, llmSeedModelID)
	if err != nil {
		return fmt.Errorf("seed the llm model: %w", err)
	}
	modelsSeeded, _ := res.RowsAffected()

	// Reported one by one: on an existing tenant the provider is usually the only one inserted, because a model of the
	// same name already sits under the operator's own provider and the unique constraint keeps it there.
	if seeded > 0 {
		fmt.Printf("seedpoc: seeded the LLM provider DeepSeek (no api key yet)\n")
	}
	if modelsSeeded > 0 {
		fmt.Printf("seedpoc: seeded the LLM model deepseek-flash (not enabled, not default)\n")
	}
	return nil
}

// allowCapability switches one platform capability on for the tenant (upsert: an existing "false" row is corrected).
func allowCapability(schema, capability string) error {
	_, err := db.DB.Exec(`INSERT INTO platform.tenant_capabilities (tenant_id, capability, is_allowed, updated_at)
		VALUES ($1, $2, true, NOW())
		ON CONFLICT (tenant_id, capability) DO UPDATE SET is_allowed = true, updated_at = NOW()`,
		schema, capability)
	return err
}

// tenantUserID returns the id of a user inside the tenant schema.
func tenantUserID(schema, name string) (string, error) {
	var id string
	err := db.DB.QueryRow(`SELECT id::text FROM `+schema+`.users WHERE name = $1`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("user %q not found in schema %s", name, schema)
	}
	return id, err
}

// ensureAPICredential makes "Authorization: Bearer <key>" work against the tenant's MCP / RPC endpoints.
//
// Two rows are involved, and both are required (see service/mcp/server/auth.go):
//   - mcp_server_configs / rpc_server_configs: the auth *scheme* must exist and be active, one row per auth_type;
//   - svc_auth_bindings: the credential itself. For apikey the issuer is empty and external_subject holds the plaintext
//     key (the login path looks it up verbatim).
//
// restrict_targets is set to false on purpose: the column defaults to true with an empty allowlist, which means "not a
// single endpoint is callable" — the opposite of what a demo box should hand out.
func ensureAPICredential(schema, userID, key string) error {
	for _, table := range []string{"mcp_server_configs", "rpc_server_configs"} {
		if _, err := db.DB.Exec(`INSERT INTO `+schema+`.`+table+`
			(id, auth_type, auth_config, is_active, created_at, updated_at)
			SELECT $1, 'apikey', '{}'::jsonb, true, NOW(), NOW()
			 WHERE NOT EXISTS (SELECT 1 FROM `+schema+`.`+table+` WHERE auth_type = 'apikey')`,
			uuid.NewString()); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		// An existing row may have been switched off; an active scheme is what makes the key usable.
		if _, err := db.DB.Exec(`UPDATE `+schema+`.`+table+`
			SET is_active = true, updated_at = NOW() WHERE auth_type = 'apikey'`); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
	}

	var bindingID string
	err := db.DB.QueryRow(`SELECT id::text FROM `+schema+`.svc_auth_bindings
		WHERE auth_type = 'apikey' AND external_issuer = '' AND external_subject = $1`, key).Scan(&bindingID)
	switch {
	case err == nil:
		if _, err := db.DB.Exec(`UPDATE `+schema+`.svc_auth_bindings
			SET user_id = $2, is_active = true, restrict_targets = false, updated_at = NOW()
			WHERE id = $1`, bindingID, userID); err != nil {
			return err
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	if _, err := db.DB.Exec(`INSERT INTO `+schema+`.svc_auth_bindings
		(id, auth_type, external_issuer, external_subject, user_id, is_active, restrict_targets, exclude_supervisor,
		 created_at, updated_at)
		VALUES ($1, 'apikey', '', $2, $3, true, false, false, NOW(), NOW())`,
		uuid.NewString(), key, userID); err != nil {
		return err
	}
	fmt.Printf("seedpoc: created an apikey binding for %s\n", schema)
	return nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seedpoc: "+format+"\n", args...)
	os.Exit(1)
}
