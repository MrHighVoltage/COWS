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
	if migrations != 26 {
		t.Fatalf("migration count = %d, want 26", migrations)
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
	if migrations != 26 {
		t.Fatalf("migration count = %d, want 26", migrations)
	}
	if err := second.Ping(); err != nil && err != sql.ErrConnDone {
		t.Fatalf("ping reopened database: %v", err)
	}
}
