package quota

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository/sqlite"
	"github.com/cows-project/cows/internal/runtime"
)

type fakeCapacity struct {
	capacity runtime.HostCapacity
	err      error
}

func (f fakeCapacity) HostCapacity(context.Context) (runtime.HostCapacity, error) {
	return f.capacity, f.err
}

func quotaTestStore(t *testing.T) (*sqlite.Store, *auth.Service, string, string) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
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
	admin, _, err := authService.Authenticate(context.Background(), "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate admin: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), admin.ID, "correct horse battery staple", "changed admin password"); err != nil {
		t.Fatalf("change admin password: %v", err)
	}
	if _, err := authService.CreateUser(context.Background(), admin.ID, auth.CreateUserInput{Username: "student", Password: "correct student password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create student: %v", err)
	}
	student, _, err := authService.Authenticate(context.Background(), "student", "correct student password")
	if err != nil {
		t.Fatalf("authenticate student: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), student.ID, "correct student password", "changed student password"); err != nil {
		t.Fatalf("change student password: %v", err)
	}
	now := time.Now().UTC()
	if err := store.CreateTemplate(context.Background(), domain.WorkspaceTemplate{ID: "template-1", Name: "Template", ImageReference: "example/image:1", DefaultCPUMillis: 1000, MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30, DefaultStorageBytes: 20 << 30, AccessMethods: []domain.AccessMethod{domain.AccessTerminal}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create test template: %v", err)
	}
	return store, authService, admin.ID, student.ID
}

func TestQuotaAssignmentAndScheduler(t *testing.T) {
	store, _, adminID, studentID := quotaTestStore(t)
	ctx := context.Background()
	service := New(store)
	assigned, err := service.Set(ctx, adminID, studentID, Input{MaxCPUMillis: 2000, MaxMemoryBytes: 4 << 30, MaxStorageBytes: 50 << 30, MaxWorkspaces: 1})
	if err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if assigned.MaxWorkspaces != 1 {
		t.Fatalf("unexpected quota: %+v", assigned)
	}
	scheduler := NewScheduler(store, fakeCapacity{capacity: runtime.HostCapacity{CPUMillis: 4000, MemoryBytes: 8 << 30, StorageBytes: 100 << 30}}, runtime.HostCapacity{})
	request := domain.ResourceRequest{CPUMillis: 1000, MemoryBytes: 2 << 30, StorageBytes: 20 << 30}
	if err := scheduler.CheckCreate(ctx, studentID, request); err != nil {
		t.Fatalf("capacity check: %v", err)
	}
	now := time.Now().UTC()
	if err := store.CreateWorkspace(ctx, domain.Workspace{ID: "workspace-1", OwnerUserID: studentID, TemplateID: "template-1", Name: "one", DesiredState: domain.DesiredWorkspaceStopped, ObservedState: "unknown", AllocatedCPUMillis: 1000, AllocatedMemoryBytes: 2 << 30, AllocatedStorageBytes: 20 << 30, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create allocated workspace: %v", err)
	}
	if err := scheduler.CheckCreate(ctx, studentID, request); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota result = %v, want quota exceeded", err)
	}
}

func TestSchedulerFailsClosed(t *testing.T) {
	store, _, adminID, studentID := quotaTestStore(t)
	service := New(store)
	if _, err := service.Set(context.Background(), adminID, studentID, Input{MaxCPUMillis: 2000, MaxMemoryBytes: 4 << 30, MaxStorageBytes: 50 << 30, MaxWorkspaces: 1}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	request := domain.ResourceRequest{CPUMillis: 1000, MemoryBytes: 2 << 30, StorageBytes: 20 << 30}
	missingCapacity := NewScheduler(store, fakeCapacity{err: runtime.ErrUnavailable}, runtime.HostCapacity{})
	if err := missingCapacity.CheckCreate(context.Background(), studentID, request); !errors.Is(err, ErrCapacityUnavailable) {
		t.Fatalf("missing capacity result = %v", err)
	}
	insufficient := NewScheduler(store, fakeCapacity{capacity: runtime.HostCapacity{CPUMillis: 1000, MemoryBytes: 1 << 30, StorageBytes: 10 << 30}}, runtime.HostCapacity{})
	if err := insufficient.CheckCreate(context.Background(), studentID, request); !errors.Is(err, ErrCapacityInsufficient) {
		t.Fatalf("insufficient capacity result = %v", err)
	}
}
