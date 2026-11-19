package provision

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"flame/nexus/shared/models"
)

// TestTemplateAppliesThroughTheDriver exercises the SQL mechanics CreateTenant depends on, through the driver production
// uses: CREATE SCHEMA, a transaction-scoped search_path, then a single Exec carrying the whole schema script followed by the
// seed script.
//
// Why it exists: verify-sql.ps1 covers the SQL text via psql, and template_language_test.go / template_pack_test.go cover the
// files, but nothing covered "lib/pq sends this multi-statement string inside a transaction and the search_path applies to
// every statement in it". That is exactly the layer where a dump-ordered script or a driver quirk breaks provisioning, and
// it is invisible to every test that does not talk to PostgreSQL.
//
// Skipped unless FLAME_TEST_DSN is set, e.g.
//
//	FLAME_TEST_DSN='postgres://postgres:pg@localhost:5432/postgres?sslmode=disable' go test ./platform/provision -run Template
//
// The test only ever creates and drops its own schema, so pointing it at a development database is fine.
func TestTemplateAppliesThroughTheDriver(t *testing.T) {
	dsn := os.Getenv("FLAME_TEST_DSN")
	if dsn == "" {
		t.Skip("FLAME_TEST_DSN is not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("failed to open the database: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("t_probe_%d", time.Now().UnixNano()%1_000_000)
	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("failed to create the probe schema: %v", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Same sequence as CreateTenant: transaction-scoped search_path, then the whole script in one Exec.
	if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, schema); err != nil {
		t.Fatalf("failed to set search_path: %v", err)
	}
	schemaSQL, err := templateAssets.ReadFile("template/schema.sql")
	if err != nil {
		t.Fatalf("failed to read template/schema.sql: %v", err)
	}
	seedSQL, err := templateAssets.ReadFile("template/seed.sql")
	if err != nil {
		t.Fatalf("failed to read template/seed.sql: %v", err)
	}
	if _, err := tx.ExecContext(ctx, string(schemaSQL)); err != nil {
		t.Fatalf("schema.sql failed through the driver: %v", err)
	}
	if _, err := tx.ExecContext(ctx, string(seedSQL)); err != nil {
		t.Fatalf("seed.sql failed through the driver: %v", err)
	}

	// The unqualified names have to resolve inside the new schema, not public: that is what the search_path buys.
	var skills int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM skills`).Scan(&skills); err != nil {
		t.Fatalf("failed to count skills: %v", err)
	}
	if want := len(models.ProtectedSkillNames()); skills != want {
		t.Errorf("seeded %d skill(s), want %d (the seed and the protected list disagree)", skills, want)
	}
	var supervised bool
	if err := tx.QueryRowContext(ctx, `SELECT is_supervisor FROM agents WHERE name = 'main'`).Scan(&supervised); err != nil {
		t.Fatalf("the seeded main agent is missing: %v", err)
	}
	if !supervised {
		t.Errorf("main.is_supervisor = false, want true")
	}
	var grants int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_tool_grants g
		  JOIN agents a ON a.id = g.agent_id
		 WHERE a.name = 'main' AND g.grant_kind = 'required'`).Scan(&grants); err != nil {
		t.Fatalf("failed to count the seeded grants: %v", err)
	}
	if grants == 0 {
		t.Errorf("main has no required tool grants: a fresh tenant would start with an agent that can do nothing")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
}
