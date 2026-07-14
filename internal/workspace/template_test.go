package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/repository/sqlite"
)

func testService(t *testing.T) (*Service, *auth.Service, string, *sqlite.Store) {
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
	return New(store), authService, adminBeforeChange.ID, store
}

func validTemplateInput() TemplateInput {
	return TemplateInput{
		Name:                            "Research Desktop",
		Description:                     "Approved research environment",
		ImageReference:                  "registry.example/research/workspace:1",
		ImageDigest:                     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DefaultCPUMillis:                1000,
		MaxCPUMillis:                    4000,
		DefaultMemoryBytes:              2 << 30,
		MaxMemoryBytes:                  8 << 30,
		DefaultStorageBytes:             20 << 30,
		InitialConnectionTimeoutSeconds: 2 * 60 * 60,
		StoppedRetentionSeconds:         24 * 60 * 60,
		AccessMethods:                   []domain.AccessMethod{domain.AccessTerminal, domain.AccessDesktop},
		AllowedRoles:                    []domain.Role{domain.RoleUser},
		Enabled:                         true,
	}
}

func TestTemplateServiceCRUDAndAuthorization(t *testing.T) {
	service, authService, adminID, _ := testService(t)
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
	service, _, adminID, _ := testService(t)
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

func TestWorkspaceCreationOwnershipAndDesiredObservedState(t *testing.T) {
	service, authService, adminID, _ := testService(t)
	ctx := context.Background()
	template, err := service.CreateTemplate(ctx, adminID, validTemplateInput())
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := authService.CreateUser(ctx, adminID, auth.CreateUserInput{Username: "student", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create student: %v", err)
	}
	student, _, err := authService.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate student: %v", err)
	}
	if err := authService.ChangePassword(ctx, student.ID, "another correct password", "changed student password"); err != nil {
		t.Fatalf("change student password: %v", err)
	}

	created, err := service.CreateWorkspace(ctx, student.ID, CreateWorkspaceInput{Name: "My research environment", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	accessMethods, err := service.WorkspaceAccessMethods(ctx, student.ID, created.ID)
	if err != nil || len(accessMethods) != 2 || accessMethods[0] != domain.AccessTerminal || accessMethods[1] != domain.AccessDesktop {
		t.Fatalf("workspace access methods: %v, %v", accessMethods, err)
	}
	if created.OwnerUserID != student.ID || created.DesiredState != domain.DesiredWorkspaceStopped || created.ObservedState != "unknown" || created.AllocatedCPUMillis != template.DefaultCPUMillis || created.InitialConnectionTimeoutSeconds != template.InitialConnectionTimeoutSeconds || created.StoppedRetentionSeconds != template.StoppedRetentionSeconds || created.DataRetentionSeconds != template.DataRetentionSeconds {
		t.Fatalf("unexpected workspace: %+v", created)
	}
	if err := service.SetDesiredState(ctx, student.ID, created.ID, domain.DesiredWorkspaceRunning); err != nil {
		t.Fatalf("set desired state: %v", err)
	}
	if err := service.UpdateObservedState(ctx, created.ID, "running", "container-123", "", time.Unix(100, 0)); err != nil {
		t.Fatalf("update observed state: %v", err)
	}
	observed, err := service.GetWorkspace(ctx, student.ID, created.ID)
	if err != nil {
		t.Fatalf("get owned workspace: %v", err)
	}
	if observed.DesiredState != domain.DesiredWorkspaceRunning || observed.ObservedState != "running" || observed.RuntimeID != "container-123" {
		t.Fatalf("unexpected observed workspace: %+v", observed)
	}
	all, err := service.ListWorkspaces(ctx, adminID)
	if err != nil || len(all) != 1 {
		t.Fatalf("administrator workspace list: count=%d err=%v", len(all), err)
	}
}

func TestTemplateConfigurationSnapshotsAndAllocatesPorts(t *testing.T) {
	service, authService, adminID, store := testService(t)
	input := validTemplateInput()
	input.Configuration = domain.TemplateConfiguration{
		Command:     []string{"/bin/sh", "-l"},
		Environment: []domain.TemplateEnvironment{{Name: "WORKSPACE_ID", Value: "{{cows.workspace_id}}"}, {Name: "DESKTOP_PORT", Value: "{{cows.service.desktop.port}}"}, {Name: "VNC_PW", Value: "{{cows.secret.vnc_password}}", Sensitive: true}, {Name: "STATIC_SECRET", Value: "{{cows.secret.fixed}}", Sensitive: true}},
		Secrets:     []domain.TemplateSecret{{Name: "vnc_password", Generate: true, Length: 8}, {Name: "fixed", Value: "static-value"}},
		Mounts:      []domain.TemplateMount{{Name: "workspace-data", ContainerPath: "/workspace"}},
		Services:    []domain.TemplateService{{Name: "desktop", Protocol: "tcp", ContainerPort: 5900, PortPool: "desktop", HostPortStart: 10000, HostPortEnd: 10099, PasswordSecret: "vnc_password"}},
	}
	template, err := service.CreateTemplate(context.Background(), adminID, input)
	if err != nil {
		t.Fatalf("create configured template: %v", err)
	}
	loaded, err := service.GetTemplate(context.Background(), adminID, template.ID)
	if err != nil || loaded.Revision != 1 || len(loaded.Configuration.Services) != 1 {
		t.Fatalf("loaded template configuration: revision=%d config=%+v err=%v", loaded.Revision, loaded.Configuration, err)
	}
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "config-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "config-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed config password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Configured workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create configured workspace: %v", err)
	}
	if value.TemplateRevision != 1 || len(value.TemplateConfiguration.Services) != 1 || len(value.TemplateSecrets["vnc_password"]) != 8 {
		t.Fatalf("workspace configuration snapshot: revision=%d config=%+v", value.TemplateRevision, value.TemplateConfiguration)
	}
	allocations, err := store.ListWorkspacePortAllocations(context.Background(), value.ID)
	if err != nil || len(allocations) != 1 || allocations[0].HostPort < 10000 || allocations[0].HostPort > 10099 {
		t.Fatalf("workspace port allocation: %+v err=%v", allocations, err)
	}
	fake := &lifecycleRuntime{}
	runtimeService := NewWithRuntime(store, fake)
	if err := runtimeService.StartWorkspace(context.Background(), user.ID, value.ID); err != nil {
		t.Fatalf("start configured workspace: %v", err)
	}
	if fake.lastSpec.NetworkMode != "bridge" || len(fake.lastSpec.Environment) != 4 || fake.lastSpec.Environment[1].Value != strconv.Itoa(allocations[0].HostPort) || fake.lastSpec.Environment[2].Name != "VNC_PW" || fake.lastSpec.Environment[2].Value != value.TemplateSecrets["vnc_password"] || !fake.lastSpec.Environment[2].Sensitive || fake.lastSpec.Environment[3].Value != "static-value" || len(fake.lastSpec.Mounts) != 1 || len(fake.lastSpec.Ports) != 1 {
		t.Fatalf("resolved runtime spec: %+v", fake.lastSpec)
	}
}

func TestTemplateConfigurationRejectsUnknownPlaceholder(t *testing.T) {
	service, _, adminID, _ := testService(t)
	input := validTemplateInput()
	input.Configuration.Environment = []domain.TemplateEnvironment{{Name: "BAD", Value: "{{cows.host_secret}}"}}
	if _, err := service.CreateTemplate(context.Background(), adminID, input); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("unknown placeholder error = %v, want invalid template", err)
	}
}
