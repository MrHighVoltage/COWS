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
	"github.com/cows-project/cows/internal/workspace"
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

func TestPasswordResetIsSingleUseAndInvalidatesSessions(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	created, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Email: "admin@example.test", Password: "correct horse battery staple"})
	if err != nil || !created {
		t.Fatalf("bootstrap administrator: created=%v err=%v", created, err)
	}
	user, session, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	request, err := service.RequestPasswordReset(ctx, "admin@example.test")
	if err != nil || request.Token == "" || request.User.ID != user.ID {
		t.Fatalf("request password reset: %+v %v", request, err)
	}
	if err := service.ResetPassword(ctx, request.Token, "new correct horse battery staple"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if _, err := service.UserForSession(ctx, session); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("session after reset = %v, want not found", err)
	}
	if _, _, err := service.Authenticate(ctx, "admin", "new correct horse battery staple"); err != nil {
		t.Fatalf("authenticate with reset password: %v", err)
	}
	if err := service.ResetPassword(ctx, request.Token, "another correct horse battery staple"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("second reset = %v, want invalid token", err)
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
	_, userToken, err := service.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate student session: %v", err)
	}
	if err := service.SetUserDisabled(ctx, admin.ID, user.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, err := service.UserForSession(ctx, userToken); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("disabled user session = %v, want not found", err)
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

func TestAdministratorCanRemoveMembershipAndDeleteGroup(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	if _, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
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
		t.Fatalf("authenticate changed administrator: %v", err)
	}
	group, err := service.CreateGroup(ctx, admin.ID, "Research", "research users")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	second, err := service.CreateGroup(ctx, admin.ID, "Other", "other users")
	if err != nil {
		t.Fatalf("create second group: %v", err)
	}
	user, err := service.CreateUser(ctx, admin.ID, CreateUserInput{Username: "student", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := service.SetUserGroups(ctx, admin.ID, user.ID, []string{group.ID, second.ID}); err != nil {
		t.Fatalf("set user groups: %v", err)
	}
	if err := service.RemoveUserFromGroup(ctx, admin.ID, user.ID, group.ID); err != nil {
		t.Fatalf("remove user group: %v", err)
	}
	groupIDs, err := service.UserGroupIDs(ctx, admin.ID, user.ID)
	if err != nil || len(groupIDs) != 1 || groupIDs[0] != second.ID {
		t.Fatalf("remaining user groups = %v, error = %v", groupIDs, err)
	}
	if err := service.DeleteGroup(ctx, admin.ID, group.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if _, err := service.FindGroupForAdmin(ctx, admin.ID, group.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted group lookup = %v", err)
	}
}

func TestGroupDeletionRejectsTemplateReferences(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	if _, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
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
		t.Fatalf("authenticate changed administrator: %v", err)
	}
	group, err := service.CreateGroup(ctx, admin.ID, "Restricted", "restricted users")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	now := time.Now().UTC()
	if err := service.store.CreateTemplate(ctx, domain.WorkspaceTemplate{ID: "template-1", Name: "Restricted template", GroupAccessMode: domain.GroupAccessInclude, AllowedGroupIDs: []string{group.ID}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create template: %v", err)
	}
	if err := service.DeleteGroup(ctx, admin.ID, group.ID); !errors.Is(err, ErrGroupInUse) {
		t.Fatalf("delete referenced group = %v, want group-in-use", err)
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

func TestRevokeOtherSessionsKeepsCallerAndRevokesStolenSession(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	if _, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, callerToken, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate caller: %v", err)
	}
	_, stolenToken, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate stolen session: %v", err)
	}
	if err := service.RevokeOtherSessions(ctx, admin.ID, callerToken); err != nil {
		t.Fatalf("revoke other sessions: %v", err)
	}
	if _, err := service.UserForSession(ctx, callerToken); err != nil {
		t.Fatalf("caller session should remain valid: %v", err)
	}
	if _, err := service.UserForSession(ctx, stolenToken); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("stolen session should be revoked, got user=%v err=%v", admin, err)
	}
}

func TestRevokeOtherSessionsWithBlankTokenRevokesAll(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	if _, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, firstToken, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate first: %v", err)
	}
	_, secondToken, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate second: %v", err)
	}
	if err := service.RevokeOtherSessions(ctx, admin.ID, ""); err != nil {
		t.Fatalf("revoke all sessions: %v", err)
	}
	if _, err := service.UserForSession(ctx, firstToken); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("first session should be revoked, got err=%v", err)
	}
	if _, err := service.UserForSession(ctx, secondToken); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("second session should be revoked, got err=%v", err)
	}
}

func TestDeleteUserRejectsWhenWorkspacesStillOwned(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.New(db)
	authService, err := New(store, time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	ctx := context.Background()
	if _, err := authService.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	adminBeforeChange, _, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := authService.ChangePassword(ctx, adminBeforeChange.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	admin, _, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	target, err := authService.CreateUser(ctx, admin.ID, CreateUserInput{Username: "still-owns-workspaces", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create target user: %v", err)
	}
	if err := authService.ChangePassword(ctx, target.ID, "another correct password", "changed target password"); err != nil {
		t.Fatalf("change target user password: %v", err)
	}

	workspaceService := workspace.New(store)
	template, err := workspaceService.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Guard Template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessTerminal}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := workspaceService.CreateWorkspace(ctx, target.ID, workspace.CreateWorkspaceInput{Name: "still-here", TemplateID: template.ID}); err != nil {
		t.Fatalf("create workspace for target: %v", err)
	}

	if err := authService.SetUserDisabled(ctx, admin.ID, target.ID, true); err != nil {
		t.Fatalf("disable target user: %v", err)
	}

	if err := authService.DeleteUser(ctx, admin.ID, target.ID); !errors.Is(err, ErrUserHasWorkspaces) {
		t.Fatalf("DeleteUser with an owned workspace error = %v, want ErrUserHasWorkspaces", err)
	}
	if _, err := authService.FindUserForAdmin(ctx, admin.ID, target.ID); err != nil {
		t.Fatalf("target user should still exist after rejected deletion: %v", err)
	}
}
