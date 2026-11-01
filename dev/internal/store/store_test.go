package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecordLifecycle(t *testing.T) {
	db, err := Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	created, err := db.Save(ctx, Record{Kind: "commands", Name: "!status", Detail: `{"mode":"text"}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	items, err := db.List(ctx, "commands")
	if err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("list returned %#v, error %v", items, err)
	}
	created.Name = "!server"
	created.Enabled = false
	updated, err := db.Save(ctx, created)
	if err != nil || updated.Name != "!server" || updated.Enabled {
		t.Fatalf("update returned %#v, error %v", updated, err)
	}
	if err := db.Delete(ctx, "commands", created.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(ctx, "commands", created.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing record error, got %v", err)
	}
}

func TestSaveRejectsInvalidInput(t *testing.T) {
	db, err := Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Save(context.Background(), Record{Kind: "unknown", Name: "x"}); err == nil {
		t.Fatal("expected unsupported kind to fail")
	}
	if _, err := db.Save(context.Background(), Record{Kind: "servers"}); err == nil {
		t.Fatal("expected empty name to fail")
	}
}

func TestSchemaMigrationIsRecorded(t *testing.T) {
	path := t.TempDir() + "/easybot.db"
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("expected schema version 2, got %d", version)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopening migrated database: %v", err)
	}
	defer db.Close()
	if err := db.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("repeated startup changed migration version to %d, error %v", version, err)
	}
}

func TestExternalDatabaseContract(t *testing.T) {
	dsn := os.Getenv("EASYBOT_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set EASYBOT_TEST_DATABASE_DSN to run the external database contract")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err := db.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("expected migration version 2, got %d, error %v", version, err)
	}

	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO users(id,username,password_hash,created_at) VALUES(?,?,?,?)`,
		uuid.NewString(), "duplicate-user", "test-hash", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateInitialAdmin(ctx, "duplicate-user", "test-password", "duplicate-session", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected duplicate username to fail")
	}
	complete, err := db.SetupComplete(ctx)
	if err != nil || complete {
		t.Fatalf("failed initialization did not roll back setup state: complete=%v error=%v", complete, err)
	}

	created, err := db.Save(ctx, Record{Kind: "servers", Name: "contract-server", Detail: `{"source":"integration-test"}`, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	items, err := db.List(ctx, "servers")
	if err != nil || len(items) != 1 || items[0].ID != created.ID || items[0].Enabled {
		t.Fatalf("list did not preserve record fields: items=%#v error=%v", items, err)
	}
	created.Name = "contract-server-updated"
	created.Enabled = true
	updated, err := db.Save(ctx, created)
	if err != nil || updated.Name != created.Name || !updated.Enabled || updated.UpdatedAt.Before(updated.CreatedAt) {
		t.Fatalf("update did not preserve boolean/timestamps: updated=%#v error=%v", updated, err)
	}

	if _, err := db.CreateInitialAdmin(ctx, "contract-admin", "test-password", "contract-session", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.AuthenticateAdmin(ctx, "contract-admin", "test-password"); err != nil || !ok {
		t.Fatalf("admin authentication failed: ok=%v error=%v", ok, err)
	}
	var sessionCount int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE token_hash=?`, "contract-session").Scan(&sessionCount); err != nil || sessionCount != 1 {
		t.Fatalf("session insert failed: count=%d error=%v", sessionCount, err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dsn)
	if err != nil {
		t.Fatalf("reopening migrated database failed: %v", err)
	}
	defer db.Close()
	if err := db.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("restart changed migration version: version=%d error=%v", version, err)
	}
	if got, err := db.List(ctx, "servers"); err != nil || len(got) != 1 || got[0].Name != "contract-server-updated" {
		t.Fatalf("record did not survive database restart: records=%#v error=%v", got, err)
	}
	if _, ok, err := db.AuthenticateAdmin(ctx, "contract-admin", "test-password"); err != nil || !ok {
		t.Fatalf("admin did not survive database restart: ok=%v error=%v", ok, err)
	}
}

func TestInitialAdminAndSessionLifecycle(t *testing.T) {
	db, err := Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	admin, err := db.CreateInitialAdmin(ctx, "admin", "a-long-test-password", "session-hash", expires)
	if err != nil {
		t.Fatal(err)
	}
	if complete, err := db.SetupComplete(ctx); err != nil || !complete {
		t.Fatalf("setup complete = %v, error = %v", complete, err)
	}
	if _, ok, err := db.AuthenticateAdmin(ctx, "admin", "a-long-test-password"); err != nil || !ok {
		t.Fatalf("valid credentials rejected: ok=%v error=%v", ok, err)
	}
	if _, ok, err := db.AuthenticateAdmin(ctx, "admin", "incorrect-password"); err != nil || ok {
		t.Fatalf("invalid credentials accepted: ok=%v error=%v", ok, err)
	}
	if _, err := db.CreateInitialAdmin(ctx, "other", "another-long-password", "other-hash", expires); err == nil {
		t.Fatal("expected initial admin creation to be one-time")
	}
	if user, ok, err := db.SessionAdmin(ctx, "session-hash"); err != nil || !ok || user.ID != admin.ID {
		t.Fatalf("session lookup returned %#v, ok=%v error=%v", user, ok, err)
	}
	if err := db.RevokeSession(ctx, "session-hash"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.SessionAdmin(ctx, "session-hash"); err != nil || ok {
		t.Fatalf("revoked session remained active: ok=%v error=%v", ok, err)
	}
}
