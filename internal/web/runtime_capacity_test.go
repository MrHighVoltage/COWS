package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/quota"
	"github.com/cows-project/cows/internal/repository/sqlite"
	"github.com/cows-project/cows/internal/runtime"
	"github.com/cows-project/cows/internal/workspace"
)

// capacityRuntime reports a fixed host size and a fixed per-container
// sample, so the capacity panel's arithmetic can be asserted exactly.
type capacityRuntime struct {
	*terminalRuntime
	usage runtime.ResourceUsage
}

func (r *capacityRuntime) HostCapacity(context.Context) (runtime.HostCapacity, error) {
	return runtime.HostCapacity{CPUMillis: 4000, MemoryBytes: 8 << 30}, nil
}

func (r *capacityRuntime) WorkspaceResourceUsage(context.Context, string) (runtime.ResourceUsage, error) {
	return r.usage, nil
}

// capacityFixture stands up an administrator with one running workspace on
// a host of 4000 mCPU / 8 GiB, overbooked 3x on CPU and 2x on memory.
type capacityFixture struct {
	server       *Server
	adminSession string
	userSession  string
	workspaceID  string
}

func newCapacityFixture(t *testing.T) capacityFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.New(db)
	authService, err := auth.New(store, time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, _, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := authService.ChangePassword(ctx, admin.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	admin, adminSession, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("re-authenticate administrator: %v", err)
	}
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "capacity-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	member, _, err := authService.Authenticate(ctx, "capacity-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(ctx, member.ID, "another correct password", "changed capacity password"); err != nil {
		t.Fatalf("change user password: %v", err)
	}
	_, userSession, err := authService.Authenticate(ctx, "capacity-user", "changed capacity password")
	if err != nil {
		t.Fatalf("re-authenticate user: %v", err)
	}

	quotaService := quota.New(store)
	if _, err := quotaService.SetHostSettings(ctx, admin.ID, quota.HostSettingsInput{CPUOverbookingFactor: 3, MemoryOverbookingFactor: 2}); err != nil {
		t.Fatalf("set host settings: %v", err)
	}

	// 150000 thousandths-of-a-percent is 150.000% of one CPU, so 1500 mCPU.
	fake := &capacityRuntime{terminalRuntime: newTerminalRuntime(), usage: runtime.ResourceUsage{CPUPercentMilli: 150000, MemoryBytes: 1 << 30, PIDs: 7}}
	service := workspace.NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Capacity template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessTerminal}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	value, err := service.CreateWorkspace(ctx, member.ID, workspace.CreateWorkspaceInput{Name: "Capacity workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.UpdateObservedState(ctx, value.ID, "running", "abcdef0123456789", "", time.Now()); err != nil {
		t.Fatalf("mark workspace running: %v", err)
	}

	server, err := New(db, authService, service, quotaService, fake, Options{SessionLifetime: time.Hour, Store: store})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	return capacityFixture{server: server, adminSession: adminSession, userSession: userSession, workspaceID: value.ID}
}

func (f capacityFixture) get(t *testing.T, path, session string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if session != "" {
		request.AddCookie(&http.Cookie{Name: "cows_session", Value: session})
	}
	recorder := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// TestAdminRuntimeCapacityReportsOverbookedAllocatableAndActualUse pins the
// two numbers the panel exists to answer: how much COWS may still allocate
// (host capacity raised by the overbooking factors) and how much the
// running containers are actually consuming.
func TestAdminRuntimeCapacityReportsOverbookedAllocatableAndActualUse(t *testing.T) {
	fixture := newCapacityFixture(t)
	recorder := fixture.get(t, "/admin/runtime/capacity", fixture.adminSession)
	if recorder.Code != http.StatusOK {
		t.Fatalf("capacity fragment status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	// 4000 mCPU x 3 overbooking, against 1000 mCPU allocated.
	if !strings.Contains(body, "1000 / 12000 mCPU allocated") {
		t.Fatalf("capacity fragment missing overbooked CPU allocatable, body = %q", body)
	}
	// 8 GiB x 2 overbooking, against the template's 2 GiB default.
	if !strings.Contains(body, "2.0 GiB / 16.0 GiB allocated") {
		t.Fatalf("capacity fragment missing overbooked memory allocatable, body = %q", body)
	}
	if !strings.Contains(body, "1500 mCPU") {
		t.Fatalf("capacity fragment missing actual CPU use, body = %q", body)
	}
	if !strings.Contains(body, "(38% of host)") {
		t.Fatalf("capacity fragment missing actual CPU use as a share of the host, body = %q", body)
	}
	if !strings.Contains(body, `hx-trigger="every 5s"`) {
		t.Fatalf("capacity fragment should re-arm its own poll, body = %q", body)
	}
}

func TestAdminRuntimeCapacityRejectsNonAdministrators(t *testing.T) {
	fixture := newCapacityFixture(t)
	recorder := fixture.get(t, "/admin/runtime/capacity", fixture.userSession)
	if recorder.Code == http.StatusOK {
		t.Fatalf("capacity fragment status = %d for a non-administrator, want a rejection", recorder.Code)
	}
}

// TestAdminRuntimeTableLinksWorkspaceAndKeepsOnlyControls covers the table
// changes: the identity cell is the way into the workspace, and the actions
// column no longer duplicates the access methods that detail view already
// offers.
func TestAdminRuntimeTableLinksWorkspaceAndKeepsOnlyControls(t *testing.T) {
	fixture := newCapacityFixture(t)
	recorder := fixture.get(t, "/admin/runtime", fixture.adminSession)
	if recorder.Code != http.StatusOK {
		t.Fatalf("runtime page status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `href="/workspaces/`+fixture.workspaceID+`/access"`) {
		t.Fatalf("runtime row should link the workspace to its detail view, body = %q", body)
	}
	if !strings.Contains(body, "Capacity workspace") {
		t.Fatalf("runtime row should name the workspace, body = %q", body)
	}
	if strings.Contains(body, "?tab=") {
		t.Fatalf("runtime row should no longer carry access buttons, body = %q", body)
	}
	if !strings.Contains(body, "1500 mCPU") {
		t.Fatalf("runtime row should report live consumption, body = %q", body)
	}
}

func TestAdminMetricsPageIsGone(t *testing.T) {
	fixture := newCapacityFixture(t)
	recorder := fixture.get(t, "/admin/metrics", fixture.adminSession)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("/admin/metrics status = %d, want 404", recorder.Code)
	}
}
