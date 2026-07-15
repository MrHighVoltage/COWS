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

func TestSelfRegistrationAppliesQuotaAndGroupsAtomically(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	store := sqlite.New(db)
	now := time.Now().UTC()
	group := domain.Group{ID: "group-research", Name: "Research", Description: "Research users", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateGroup(context.Background(), group); err != nil {
		t.Fatalf("create default group: %v", err)
	}
	service, err := New(store, time.Hour, RegistrationPolicy{Enabled: true, DefaultGroupNames: []string{"Research"}, DefaultQuota: domain.UserQuota{MaxCPUMillis: 2000, MaxMemoryBytes: 4 << 30, MaxStorageBytes: 20 << 30, MaxWorkspaces: 2, MaxRunningWorkspaces: 1}})
	if err != nil {
		t.Fatalf("create registration service: %v", err)
	}
	ctx := context.Background()
	user, err := service.Register(ctx, RegisterUserInput{Username: "new-user", Email: "new@example.test", DisplayName: "New User", Password: "a correct registration password", PasswordConfirmation: "a correct registration password"})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	if user.Role != domain.RoleUser || user.MustChangePassword || user.Email != "new@example.test" {
		t.Fatalf("registered user = %+v", user)
	}
	assigned, err := store.FindUserQuota(ctx, user.ID)
	if err != nil || assigned.MaxStorageBytes != 20<<30 || assigned.MaxRunningWorkspaces != 1 {
		t.Fatalf("registered quota = %+v err=%v", assigned, err)
	}
	groups, err := store.ListUserGroupIDs(ctx, user.ID)
	if err != nil || len(groups) != 1 || groups[0] != group.ID {
		t.Fatalf("registered groups = %v err=%v", groups, err)
	}
	if _, _, err := service.Authenticate(ctx, "new-user", "a correct registration password"); err != nil {
		t.Fatalf("authenticate registered user: %v", err)
	}
}

func TestSelfRegistrationFailsClosedWhenDefaultGroupIsMissing(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	store := sqlite.New(db)
	service, err := New(store, time.Hour, RegistrationPolicy{Enabled: true, DefaultGroupNames: []string{"missing"}, DefaultQuota: domain.UserQuota{MaxWorkspaces: 1}})
	if err != nil {
		t.Fatalf("create registration service: %v", err)
	}
	_, err = service.Register(context.Background(), RegisterUserInput{Username: "new-user", Email: "new@example.test", Password: "a correct registration password", PasswordConfirmation: "a correct registration password"})
	if !errors.Is(err, ErrRegistrationUnavailable) {
		t.Fatalf("missing default group error = %v", err)
	}
	count, err := store.CountUsers(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("users after failed registration = %d err=%v", count, err)
	}
}
