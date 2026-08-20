package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/repository/sqlite"
)

// defaultDatabasePath mirrors the COWS_DATABASE_PATH default in
// internal/config so an operator running the recovery command on a default
// installation does not have to repeat the path.
const defaultDatabasePath = "./data/cows.db"

// runRecoverAdmin is the offline administrator credential-recovery entry point.
// It deliberately touches nothing but the database: no HTTP server, no Podman
// runtime, and no background loop, so it is safe to run on a host whose COWS
// service is down. Its trust boundary is local access to the database file.
func runRecoverAdmin(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cows recover-admin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("database", envOrDefault("COWS_DATABASE_PATH", defaultDatabasePath), "path to the COWS SQLite database")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: cows recover-admin [-database PATH] USERNAME")
		fmt.Fprintln(stderr, "\nResets a named administrator's password to a generated temporary one,")
		fmt.Fprintln(stderr, "requires a password change at the next login, and invalidates every")
		fmt.Fprintln(stderr, "session for that account.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return errors.New("recover-admin takes exactly one administrator username")
	}
	username := flags.Arg(0)

	db, err := database.Open(ctx, *databasePath)
	if err != nil {
		return fmt.Errorf("open database %s: %w", *databasePath, err)
	}
	defer db.Close()

	authService, err := auth.New(sqlite.New(db), time.Hour, auth.RegistrationPolicy{})
	if err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}
	password, err := authService.RecoverAdministrator(ctx, username)
	if errors.Is(err, auth.ErrRecoveryTargetInvalid) {
		return fmt.Errorf("%q is not an existing, enabled administrator", username)
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Temporary password for administrator %q: %s\n", username, password)
	fmt.Fprintln(stdout, "It is shown once and is not stored anywhere in plaintext.")
	fmt.Fprintln(stdout, "All existing sessions for this account were invalidated, and COWS will")
	fmt.Fprintln(stdout, "require a new password at the next login.")
	return nil
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
