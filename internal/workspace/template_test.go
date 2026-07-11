package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/repository/sqlite"
)

func testService(t *testing.T) (*Service, *auth.Service, string) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.New(db)
	authService, err := auth.New(store, time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	if _, err := authService.BootstrapAdministrator(context.Background(), auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	adminBeforeChange, _, err := authService.Authenticate(context.Background(), "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), adminBeforeChange.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	return New(store), authService, adminBeforeChange.ID
}

func validTemplateInput() TemplateInput {
	return TemplateInput{
		Name:                "Research Desktop",
		Description:         "Approved research environment",
		ImageReference:      "registry.example/research/workspace:1",
		ImageDigest:         "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DefaultCPUMillis:    1000,
		MaxCPUMillis:        4000,
		DefaultMemoryBytes:  2 << 30,
		MaxMemoryBytes:      8 << 30,
		DefaultStorageBytes: 20 << 30,
		AccessMethods:       []domain.AccessMethod{domain.AccessTerminal, domain.AccessDesktop},
		AllowedRoles:        []domain.Role{domain.RoleUser},
		Enabled:             true,
	}
}

func TestTemplateServiceCRUDAndAuthorization(t *testing.T) {
	service, authService, adminID := testService(t)
	ctx := context.Background()
	created, err := service.CreateTemplate(ctx, adminID, validTemplateInput())
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if created.ID == "" || created.Name != "Research Desktop" || len(created.AccessMethods) != 2 {
		t.Fatalf("unexpected created template: %+v", created)
	}
	list, err := service.ListTemplates(ctx, adminID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list templates: count=%d err=%v", len(list), err)
	}
	updatedInput := validTemplateInput()
	updatedInput.Name = "Updated Research Desktop"
	updatedInput.Enabled = false
	updated, err := service.UpdateTemplate(ctx, adminID, created.ID, updatedInput)
	if err != nil || updated.Name != updatedInput.Name || updated.Enabled {
		t.Fatalf("update template: %+v err=%v", updated, err)
	}
	if err := service.SetTemplateEnabled(ctx, adminID, created.ID, true); err != nil {
		t.Fatalf("enable template: %v", err)
	}
	if _, err := service.CreateTemplate(ctx, "not-an-admin", validTemplateInput()); err == nil {
		t.Fatal("non-administrator created a template")
	}
	if _, err := authService.CreateUser(ctx, adminID, auth.CreateUserInput{Username: "student", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create student: %v", err)
	}
	student, _, err := authService.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate student: %v", err)
	}
	if _, err := service.ListTemplates(ctx, student.ID); err == nil {
		t.Fatal("non-administrator listed templates")
	}
}

func TestTemplateValidationAndDuplicateName(t *testing.T) {
	service, _, adminID := testService(t)
	ctx := context.Background()
	invalid := validTemplateInput()
	invalid.MaxCPUMillis = invalid.DefaultCPUMillis - 1
	if _, err := service.CreateTemplate(ctx, adminID, invalid); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("invalid resource limits error = %v", err)
	}
	created, err := service.CreateTemplate(ctx, adminID, validTemplateInput())
	if err != nil {
		t.Fatalf("create first template: %v", err)
	}
	duplicate := validTemplateInput()
	duplicate.Name = " research desktop "
	if _, err := service.CreateTemplate(ctx, adminID, duplicate); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate name error = %v", err)
	}
	if _, err := service.GetTemplate(ctx, adminID, created.ID); err != nil {
		t.Fatalf("get template: %v", err)
	}
}
