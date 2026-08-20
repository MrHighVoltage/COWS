package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository/sqlite"
)

func recoverTestDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cows.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	service, err := auth.New(sqlite.New(db), time.Hour, auth.RegistrationPolicy{})
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	if _, err := service.BootstrapAdministrator(context.Background(), auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	return path
}

func TestRecoverAdminPrintsAWorkingTemporaryPassword(t *testing.T) {
	path := recoverTestDatabase(t)
	var stdout, stderr bytes.Buffer
	if err := runRecoverAdmin(context.Background(), []string{"-database", path, "admin"}, &stdout, &stderr); err != nil {
		t.Fatalf("recover-admin: %v (stderr %q)", err, stderr.String())
	}

	const prefix = "Temporary password for administrator \"admin\": "
	line, _, found := strings.Cut(strings.TrimPrefix(stdout.String(), prefix), "\n")
	if !found || line == "" {
		t.Fatalf("stdout did not report a temporary password: %q", stdout.String())
	}

	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()
	service, err := auth.New(sqlite.New(db), time.Hour, auth.RegistrationPolicy{})
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	user, _, err := service.Authenticate(context.Background(), "admin", line)
	if err != nil {
		t.Fatalf("authenticate with the printed password: %v", err)
	}
	if user.Role != domain.RoleAdministrator || !user.MustChangePassword {
		t.Fatalf("recovered user = %+v, want an administrator required to change the password", user)
	}
}

func TestRecoverAdminRejectsBadInvocations(t *testing.T) {
	path := recoverTestDatabase(t)
	cases := map[string][]string{
		"no username":      {"-database", path},
		"two usernames":    {"-database", path, "admin", "other"},
		"unknown username": {"-database", path, "absent"},
		"missing database": {"-database", filepath.Join(t.TempDir(), "nested", "missing.db"), "admin"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := runRecoverAdmin(context.Background(), args, &stdout, &stderr); err == nil {
				t.Fatalf("expected an error, got stdout %q", stdout.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("a failed recovery wrote to stdout: %q", stdout.String())
			}
		})
	}
}
