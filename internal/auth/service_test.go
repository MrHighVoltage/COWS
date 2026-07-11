package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/repository/sqlite"
)

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	service, err := New(sqlite.New(db), time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	return service
}

func TestBootstrapAuthenticateAndSession(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	created, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", DisplayName: "Administrator", Password: "correct horse battery staple", Role: domain.RoleUser})
	if err != nil || !created {
		t.Fatalf("bootstrap administrator: created=%v err=%v", created, err)
	}
	created, err = service.BootstrapAdministrator(ctx, CreateUserInput{Username: "other", Password: "correct horse battery staple"})
	if err != nil || created {
		t.Fatalf("second bootstrap: created=%v err=%v", created, err)
	}

	user, token, err := service.Authenticate(ctx, "ADMIN", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if user.Role != domain.RoleAdministrator || token == "" {
		t.Fatalf("unexpected authenticated user/token: %+v %q", user, token)
	}
	if !user.MustChangePassword {
		t.Fatal("new administrator should require a password change")
	}
	if err := service.ChangePassword(ctx, user.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change bootstrap password: %v", err)
	}
	sessionUser, err := service.UserForSession(ctx, token)
	if err != nil || sessionUser.ID != user.ID {
		t.Fatalf("session user: %+v %v", sessionUser, err)
	}
	if err := service.Logout(ctx, token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := service.UserForSession(ctx, token); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("session after logout: %v", err)
	}
}

func TestAdministratorCanManageUsersWithSafetyChecks(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	_, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	adminBeforeChange, _, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := service.ChangePassword(ctx, adminBeforeChange.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	admin, _, err := service.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate changed administrator password: %v", err)
	}
	user, err := service.CreateUser(ctx, admin.ID, CreateUserInput{Username: "student", DisplayName: "Student", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := service.SetUserDisabled(ctx, admin.ID, user.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, _, err := service.Authenticate(ctx, "student", "another correct password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled user authentication error = %v", err)
	}
	if err := service.SetUserDisabled(ctx, admin.ID, admin.ID, true); !errors.Is(err, ErrSelfDisable) {
		t.Fatalf("self-disable error = %v", err)
	}
	if err := service.SetUserDisabled(ctx, admin.ID, admin.ID, false); err != nil {
		t.Fatalf("re-enable administrator: %v", err)
	}
	if err := service.SetUserDisabled(ctx, admin.ID, user.ID, false); err != nil {
		t.Fatalf("re-enable user: %v", err)
	}
}

func TestValidation(t *testing.T) {
	service := testService(t)
	_, err := service.BootstrapAdministrator(context.Background(), CreateUserInput{Username: "ab", Password: "short"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("validation error = %v", err)
	}
}
