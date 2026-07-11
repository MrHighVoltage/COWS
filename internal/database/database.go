package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// migrationFiles is embedded so the executable does not depend on its working
// directory after installation.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := ensureDatabaseFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := configure(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func ensureDatabaseFile(path string) error {
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close database file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set database file permissions: %w", err)
	}
	return nil
}

func configure(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite with %q: %w", statement, err)
		}
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("find migrations: %w", err)
	}
	sort.Strings(entries)
	for _, entry := range entries {
		version, err := migrationVersion(entry)
		if err != nil {
			return err
		}
		var applied bool
		if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = ?)", version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied {
			continue
		}

		sqlText, err := migrationFiles.ReadFile(entry)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(sqlText)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, datetime('now'))", version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

func migrationVersion(path string) (int, error) {
	name := filepath.Base(path)
	prefix := strings.SplitN(name, "_", 2)[0]
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("invalid migration filename %q: %w", name, err)
	}
	return version, nil
}
