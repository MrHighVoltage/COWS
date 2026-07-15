package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
)

func TestBeginFileAccessAllowsStoppedWorkspace(t *testing.T) {
	service, authService, adminID, store := testService(t)
	input := validTemplateInput()
	input.AccessMethods = []domain.AccessMethod{domain.AccessFiles}
	input.Configuration.Mounts = []domain.TemplateMount{{Name: "data", Type: domain.TemplateMountDirectory, ContainerPath: "/data", FileManager: true}}
	template, err := service.CreateTemplate(context.Background(), adminID, input)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	service = NewWithRuntimeAndMountRoot(store, &lifecycleRuntime{}, t.TempDir())
	if _, err := authService.CreateUser(context.Background(), adminID, auth.CreateUserInput{Username: "stopped-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, _, err := authService.Authenticate(context.Background(), "stopped-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(context.Background(), user.ID, "another correct password", "changed stopped password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	value, err := service.CreateWorkspace(context.Background(), user.ID, CreateWorkspaceInput{Name: "Stopped workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.UpdateWorkspaceObservedState(context.Background(), value.ID, "stopped", "runtime-stopped", "", "", value.CreatedAt, value.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	release, err := service.BeginFileAccess(context.Background(), user.ID, value.ID)
	if err != nil {
		t.Fatalf("begin stopped file access: %v", err)
	}
	startContext, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := service.StartWorkspace(startContext, user.ID, value.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start during file access error = %v, want deadline exceeded", err)
	}
	release()

	if err := store.UpdateWorkspaceObservedState(context.Background(), value.ID, "unknown", "runtime-stopped", "", "", value.CreatedAt, value.CreatedAt); err != nil {
		t.Fatalf("set unknown state: %v", err)
	}
	if _, err := service.BeginFileAccess(context.Background(), user.ID, value.ID); !errors.Is(err, ErrFileManagerNotAvailable) {
		t.Fatalf("unknown-state file access error = %v", err)
	}
}
