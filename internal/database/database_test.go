package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenInitializesSQLite(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign key setting: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign keys = %d, want 1", foreignKeys)
	}

	var migrations int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != 27 {
		t.Fatalf("migration count = %d, want 27", migrations)
	}

	var metadataTable string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'app_metadata'").Scan(&metadataTable); err != nil {
		t.Fatalf("find metadata table: %v", err)
	}
	if metadataTable != "app_metadata" {
		t.Fatalf("metadata table = %q", metadataTable)
	}
}

func TestOpenReusesAppliedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cows.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open first database: %v", err)
	}
	first.Close()

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open second database: %v", err)
	}
	defer second.Close()

	var migrations int
	if err := second.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != 27 {
		t.Fatalf("migration count = %d, want 27", migrations)
	}
	if err := second.Ping(); err != nil && err != sql.ErrConnDone {
		t.Fatalf("ping reopened database: %v", err)
	}
}

// Migration 0026 added idle_since with DEFAULT 0, which makes a workspace that
// was already running when it was applied look "never idle" and exempts it from
// the idle stop for good. Migration 0027 seeds those rows from started_at.
func TestBackfillMigrationSeedsIdleSinceFromStartedAt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cows.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	// Insert directly, without the owner and template rows the foreign keys
	// require: this test is about the backfill statement, not the schema.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	insert := `INSERT INTO workspaces
		(id, owner_user_id, template_id, name, desired_state, observed_state,
		 allocated_cpu_millis, allocated_memory_bytes, allocated_storage_bytes,
		 created_at, updated_at, started_at, active_sessions, idle_since)
		VALUES (?, 'owner-1', 'template-1', ?, 'running', ?, 1000, 1, 1, 100, 100, ?, ?, 0)`
	rows := []struct {
		id             string
		observed       string
		startedAt      int64
		activeSessions int
	}{
		{"running-idle", "running", 500, 0},
		{"running-connected", "running", 500, 1},
		{"stopped", "stopped", 500, 0},
		{"never-started", "running", 0, 0},
	}
	for _, row := range rows {
		if _, err := conn.ExecContext(ctx, insert, row.id, row.id, row.observed, row.startedAt, row.activeSessions); err != nil {
			t.Fatalf("insert %s: %v", row.id, err)
		}
	}
	// Re-run migration 0027 by forgetting that it was applied.
	if _, err := conn.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 27"); err != nil {
		t.Fatalf("reset migration: %v", err)
	}
	conn.Close()
	db.Close()

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer reopened.Close()

	want := map[string]int64{"running-idle": 500, "running-connected": 0, "stopped": 0, "never-started": 0}
	for id, expected := range want {
		var idleSince int64
		if err := reopened.QueryRowContext(ctx, "SELECT idle_since FROM workspaces WHERE id = ?", id).Scan(&idleSince); err != nil {
			t.Fatalf("read idle_since for %s: %v", id, err)
		}
		if idleSince != expected {
			t.Fatalf("idle_since for %s = %d, want %d", id, idleSince, expected)
		}
	}
}
