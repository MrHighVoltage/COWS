package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/quota"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/repository/sqlite"
	"github.com/cows-project/cows/internal/workspace"
)

func testServer(t *testing.T) (*Server, *auth.Service) {
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
	templateService := workspace.New(store)
	server, err := New(db, authService, templateService, quota.New(store), nil, Options{SessionLifetime: time.Hour})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	return server, authService
}

func TestQuotaProgressClass(t *testing.T) {
	tests := []struct {
		name    string
		current int64
		limit   int64
		want    string
	}{
		{name: "unassigned", current: 10, limit: 0, want: ""},
		{name: "unlimited", current: 10, limit: -1, want: ""},
		{name: "normal", current: 79, limit: 100, want: ""},
		{name: "warning", current: 80, limit: 100, want: "quota-progress-warning"},
		{name: "full", current: 100, limit: 100, want: "quota-progress-danger"},
		{name: "over limit", current: 120, limit: 100, want: "quota-progress-danger"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := quotaProgressClass(test.current, test.limit); got != test.want {
				t.Fatalf("quotaProgressClass(%d, %d) = %q, want %q", test.current, test.limit, got, test.want)
			}
		})
	}
}

func TestWorkspaceActionReturnPathKeepsAdministratorRuntimeContext(t *testing.T) {
	admin := &domain.User{Role: domain.RoleAdministrator}
	user := &domain.User{Role: domain.RoleUser}

	request := httptest.NewRequest(http.MethodPost, "/workspaces/id/stop", strings.NewReader("return_to=/admin/runtime"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := classifyWorkspaceActionContext(request, admin).redirectPath("id"); got != "/admin/runtime" {
		t.Fatalf("administrator return path = %q, want /admin/runtime", got)
	}
	if got := classifyWorkspaceActionContext(request, user).redirectPath("id"); got != "/workspaces" {
		t.Fatalf("user return path = %q, want /workspaces", got)
	}

	request = httptest.NewRequest(http.MethodPost, "/workspaces/id/stop", strings.NewReader("return_to=https://example.test"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := classifyWorkspaceActionContext(request, admin).redirectPath("id"); got != "/workspaces" {
		t.Fatalf("external return path = %q, want /workspaces", got)
	}

	request = httptest.NewRequest(http.MethodPost, "/workspaces/id/stop", strings.NewReader("context=detail"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := classifyWorkspaceActionContext(request, user).redirectPath("id"); got != "/workspaces/id/access" {
		t.Fatalf("detail return path = %q, want /workspaces/id/access", got)
	}
}

func TestWorkspaceActionErrorExplainsQuotaAndCapacity(t *testing.T) {
	quotaMessage := workspaceActionError("start", &quota.QuotaInsufficientError{Resource: "CPU", Current: 1000, Limit: 2000, Requested: 1500})
	if !strings.Contains(quotaMessage, "assigned CPU quota") || !strings.Contains(quotaMessage, "1000 mCPU") {
		t.Fatalf("quota message = %q, want actionable CPU details", quotaMessage)
	}
	capacityMessage := workspaceActionError("start", &quota.CapacityInsufficientError{Resource: "memory", Available: 1 << 30, Requested: 2 << 30})
	if !strings.Contains(capacityMessage, "host") || !strings.Contains(capacityMessage, "1024 MiB") || !strings.Contains(capacityMessage, "2048 MiB") {
		t.Fatalf("capacity message = %q, want actionable memory details", capacityMessage)
	}
}

// rowActionRuntime adds working Stop/Remove to the terminal-focused fake
// runtime (whose own Stop/Remove just stub out ErrNotSupported), so the
// dynamic row-action test can exercise a real state transition.
type rowActionRuntime struct{ *terminalRuntime }

func (r *rowActionRuntime) StopWorkspace(context.Context, string, time.Duration) error { return nil }
func (r *rowActionRuntime) RemoveWorkspace(context.Context, string) error              { return nil }

// TestWorkspaceActionHTMXRequestSwapsRowInPlace covers the dynamic row-action
// behavior: an htmx-triggered request gets back just the <tr> fragment
// instead of a full page, with the outcome (success or a rejected action)
// reflected in that same row rather than a page-level banner.
func TestWorkspaceActionHTMXRequestSwapsRowInPlace(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
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
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "row-action-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, sessionToken, err := authService.Authenticate(ctx, "row-action-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(ctx, user.ID, "another correct password", "changed row-action password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	user, sessionToken, err = authService.Authenticate(ctx, "row-action-user", "changed row-action password")
	if err != nil {
		t.Fatalf("re-authenticate user: %v", err)
	}

	fake := &rowActionRuntime{terminalRuntime: newTerminalRuntime()}
	service := workspace.NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Row action template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessTerminal}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	value, err := service.CreateWorkspace(ctx, user.ID, workspace.CreateWorkspaceInput{Name: "Row action workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.UpdateObservedState(ctx, value.ID, "running", "abcdef0123456789", "", time.Now()); err != nil {
		t.Fatalf("mark workspace running: %v", err)
	}

	server, err := New(db, authService, service, nil, fake, Options{SessionLifetime: time.Hour})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}

	const csrfToken = "test-csrf-token-0123456789abcdef"
	postRowAction := func(action string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/workspaces/"+value.ID+"/"+action, strings.NewReader("csrf_token="+csrfToken))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("HX-Request", "true")
		request.AddCookie(&http.Cookie{Name: "cows_session", Value: sessionToken})
		request.AddCookie(&http.Cookie{Name: "cows_csrf", Value: csrfToken})
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	// Delete is rejected while running: the row must come back with the
	// rejection message, not an empty (row-removed) response, and the
	// workspace must still exist.
	deleteRecorder := postRowAction("delete")
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete-while-running status = %d, want 200", deleteRecorder.Code)
	}
	deleteBody := deleteRecorder.Body.String()
	if !strings.Contains(deleteBody, `id="workspace-row-`+value.ID+`"`) {
		t.Fatalf("delete-while-running response missing the row, body = %q", deleteBody)
	}
	if !strings.Contains(deleteBody, "not in a state where it can be deleted") {
		t.Fatalf("delete-while-running response missing the rejection message, body = %q", deleteBody)
	}
	if _, err := service.GetWorkspace(ctx, user.ID, value.ID); err != nil {
		t.Fatalf("workspace should still exist after a rejected delete: %v", err)
	}

	// Stop succeeds: the row comes back reflecting the new state, and the
	// access links for a stopped workspace are gone.
	stopRecorder := postRowAction("stop")
	if stopRecorder.Code != http.StatusOK {
		t.Fatalf("stop status = %d, want 200", stopRecorder.Code)
	}
	stopBody := stopRecorder.Body.String()
	if !strings.Contains(stopBody, `id="workspace-row-`+value.ID+`"`) {
		t.Fatalf("stop response missing the row, body = %q", stopBody)
	}
	if !strings.Contains(stopBody, "status-dot-neutral") {
		t.Fatalf("stop response should show the stopped state stamp, body = %q", stopBody)
	}
	// The strip rides along out of band, so the running count and resource
	// totals move at the same moment the row does.
	if !strings.Contains(stopBody, `id="workspace-resource-strip"`) || !strings.Contains(stopBody, `hx-swap-oob="true"`) {
		t.Fatalf("stop response should carry the out-of-band resource strip, body = %q", stopBody)
	}

	// Delete now succeeds (workspace is stopped): the response carries no
	// row so htmx removes it, only the out-of-band resource strip whose
	// totals the delete changed, and the workspace is actually gone.
	finalDeleteRecorder := postRowAction("delete")
	if finalDeleteRecorder.Code != http.StatusOK {
		t.Fatalf("final delete status = %d, want 200", finalDeleteRecorder.Code)
	}
	finalDeleteBody := finalDeleteRecorder.Body.String()
	if strings.Contains(finalDeleteBody, "<tr") {
		t.Fatalf("final delete response should carry no row (row removed), got %q", finalDeleteBody)
	}
	if !strings.Contains(finalDeleteBody, `id="workspace-resource-strip"`) || !strings.Contains(finalDeleteBody, `hx-swap-oob="true"`) {
		t.Fatalf("final delete response should carry the out-of-band resource strip, got %q", finalDeleteBody)
	}
	if _, err := service.GetWorkspace(ctx, user.ID, value.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("workspace should be gone after delete, err = %v", err)
	}
}

// TestWorkspaceActionHTMXDetailContextSwapsHeaderInPlace covers the
// Workspace Detail page's header lifecycle actions: an htmx request with
// context=detail gets back the header fragment (not a table row), and a
// successful delete redirects the client to the Overview via HX-Redirect
// instead of leaving an empty fragment on a now-gone workspace's own page.
func TestWorkspaceActionHTMXDetailContextSwapsHeaderInPlace(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
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
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "detail-action-user", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, sessionToken, err := authService.Authenticate(ctx, "detail-action-user", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	if err := authService.ChangePassword(ctx, user.ID, "another correct password", "changed detail-action password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	user, sessionToken, err = authService.Authenticate(ctx, "detail-action-user", "changed detail-action password")
	if err != nil {
		t.Fatalf("re-authenticate user: %v", err)
	}

	fake := &rowActionRuntime{terminalRuntime: newTerminalRuntime()}
	service := workspace.NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Detail action template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessTerminal}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	value, err := service.CreateWorkspace(ctx, user.ID, workspace.CreateWorkspaceInput{Name: "Detail action workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.UpdateObservedState(ctx, value.ID, "running", "abcdef0123456789", "", time.Now()); err != nil {
		t.Fatalf("mark workspace running: %v", err)
	}

	server, err := New(db, authService, service, nil, fake, Options{SessionLifetime: time.Hour})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}

	const csrfToken = "test-csrf-token-0123456789abcdef"
	postDetailAction := func(action string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/workspaces/"+value.ID+"/"+action, strings.NewReader("csrf_token="+csrfToken+"&context=detail"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("HX-Request", "true")
		request.AddCookie(&http.Cookie{Name: "cows_session", Value: sessionToken})
		request.AddCookie(&http.Cookie{Name: "cows_csrf", Value: csrfToken})
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	// Delete is rejected while running: the header must come back with the
	// rejection message, not a redirect, and the workspace must still exist.
	deleteRecorder := postDetailAction("delete")
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete-while-running status = %d, want 200", deleteRecorder.Code)
	}
	if got := deleteRecorder.Header().Get("HX-Redirect"); got != "" {
		t.Fatalf("delete-while-running should not redirect, got HX-Redirect = %q", got)
	}
	deleteBody := deleteRecorder.Body.String()
	if !strings.Contains(deleteBody, `id="workspace-detail-header"`) {
		t.Fatalf("delete-while-running response missing the header, body = %q", deleteBody)
	}
	if !strings.Contains(deleteBody, "not in a state where it can be deleted") {
		t.Fatalf("delete-while-running response missing the rejection message, body = %q", deleteBody)
	}
	if _, err := service.GetWorkspace(ctx, user.ID, value.ID); err != nil {
		t.Fatalf("workspace should still exist after a rejected delete: %v", err)
	}

	// Stop succeeds: the header comes back reflecting the new state (Start
	// button only, no Stop/Restart).
	stopRecorder := postDetailAction("stop")
	if stopRecorder.Code != http.StatusOK {
		t.Fatalf("stop status = %d, want 200", stopRecorder.Code)
	}
	stopBody := stopRecorder.Body.String()
	if !strings.Contains(stopBody, `id="workspace-detail-header"`) {
		t.Fatalf("stop response missing the header, body = %q", stopBody)
	}
	if !strings.Contains(stopBody, "status-dot-neutral") {
		t.Fatalf("stop response should show the stopped state stamp, body = %q", stopBody)
	}
	if strings.Contains(stopBody, ">Stop<") {
		t.Fatalf("stop response should not still offer Stop, body = %q", stopBody)
	}

	// Delete now succeeds (workspace is stopped): the response redirects to
	// the Overview via HX-Redirect, and the workspace is actually gone.
	finalDeleteRecorder := postDetailAction("delete")
	if finalDeleteRecorder.Code != http.StatusOK {
		t.Fatalf("final delete status = %d, want 200", finalDeleteRecorder.Code)
	}
	if got := finalDeleteRecorder.Header().Get("HX-Redirect"); got != "/workspaces" {
		t.Fatalf("final delete HX-Redirect = %q, want /workspaces", got)
	}
	if _, err := service.GetWorkspace(ctx, user.ID, value.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("workspace should be gone after delete, err = %v", err)
	}
}

func TestFileDeleteParent(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "file.txt", want: "."},
		{path: "folder/file.txt", want: "folder"},
		{path: "one/two/folder", want: "one/two"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := fileDeleteParent(test.path); got != test.want {
				t.Fatalf("fileDeleteParent(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestHomeRendersLocalServerDrivenPage(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	server, _ := testServer(t)
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	for _, expected := range []string{"COWS", "/static/vendor/htmx/htmx.min.js", "/static/vendor/alpine/alpine.min.js", `hx-get="/fragments/health"`} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Errorf("page does not contain %q", expected)
		}
	}
	for header, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestEnabledRegistrationRendersAndCreatesUser(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	store := sqlite.New(db)
	authService, err := auth.New(store, time.Hour, auth.RegistrationPolicy{Enabled: true, DefaultQuota: domain.UserQuota{MaxWorkspaces: 2, MaxRunningWorkspaces: 1}})
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	server, err := New(db, authService, workspace.New(store), quota.New(store), nil, Options{SessionLifetime: time.Hour, RegistrationEnabled: true})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, httptest.NewRequest(http.MethodGet, "/register", nil))
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil || !strings.Contains(pageRecorder.Body.String(), "Create account") {
		t.Fatalf("registration page: status=%d csrf=%#v body=%s", pageRecorder.Code, csrfCookie, pageRecorder.Body.String())
	}
	form := url.Values{"csrf_token": {csrfCookie.Value}, "username": {"new-user"}, "email": {"new@example.test"}, "display_name": {"New User"}, "password": {"a correct registration password"}, "confirm_password": {"a correct registration password"}}
	request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Your account was created") {
		t.Fatalf("registration response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, _, err := authService.Authenticate(context.Background(), "new-user", "a correct registration password"); err != nil {
		t.Fatalf("authenticate registered user: %v", err)
	}
}

func TestHealthEndpoint(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	server, _ := testServer(t)
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if response["status"] != "ok" || response["database"] != "ok" {
		t.Fatalf("unexpected health response: %#v", response)
	}
}

func TestReadyEndpointReportsOKWhenDatabaseAndRuntimeAreHealthy(t *testing.T) {
	server, _ := testServer(t)
	server.runtime = newTerminalRuntime()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if response["status"] != "ok" || response["database"] != "ok" || response["runtime"] != "ok" {
		t.Fatalf("unexpected readiness response: %#v", response)
	}
}

func TestReadyEndpointReportsDegradedWhenRuntimeIsUnset(t *testing.T) {
	server, _ := testServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if response["status"] != "degraded" || response["runtime"] != "unavailable" || response["database"] != "ok" {
		t.Fatalf("unexpected readiness response: %#v", response)
	}
}

func TestReadyEndpointReportsDegradedWhenRuntimeNameFails(t *testing.T) {
	server, _ := testServer(t)
	server.runtime = &failingNameRuntime{terminalRuntime: newTerminalRuntime()}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if response["status"] != "degraded" || response["runtime"] != "unavailable" {
		t.Fatalf("unexpected readiness response: %#v", response)
	}
}

type failingNameRuntime struct {
	*terminalRuntime
}

func (r *failingNameRuntime) Name(context.Context) (string, error) {
	return "", errors.New("runtime socket unreachable")
}

func TestHealthFragmentRendersHTML(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/fragments/health", nil)
	server, _ := testServer(t)
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `id="health-status"`) {
		t.Fatalf("unexpected fragment response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestStateChangingRequestsRequireCSRF(t *testing.T) {
	server, _ := testServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF response = %d, want 403", recorder.Code)
	}
}

func TestLoginRateLimitReturnsRetryAfter(t *testing.T) {
	server, authService := testServer(t)
	limiter, err := auth.NewLoginLimiter(1, time.Hour)
	if err != nil {
		t.Fatalf("create login limiter: %v", err)
	}
	server.options.LoginLimiter = limiter
	if _, err := authService.BootstrapAdministrator(context.Background(), auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	loginPage := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/login", nil))
	csrfCookie := cookieByName(loginPage.Result().Cookies(), "cows_csrf")
	if csrfCookie == nil {
		t.Fatal("login page did not set CSRF cookie")
	}
	failedLogin := func() *httptest.ResponseRecorder {
		form := url.Values{"csrf_token": {csrfCookie.Value}, "username": {"admin"}, "password": {"wrong password"}}
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(csrfCookie)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if response := failedLogin(); response.Code != http.StatusUnauthorized {
		t.Fatalf("first failed login status = %d", response.Code)
	}
	response := failedLogin()
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("rate-limited login response: status=%d retry-after=%q", response.Code, response.Header().Get("Retry-After"))
	}
}

func TestLoginAndAdministratorUserManagement(t *testing.T) {
	server, authService := testServer(t)
	ctx := context.Background()
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}

	loginRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login page status = %d", loginRecorder.Code)
	}
	csrfCookie := cookieByName(loginRecorder.Result().Cookies(), "cows_csrf")
	if csrfCookie == nil {
		t.Fatal("login page did not set CSRF cookie")
	}

	loginForm := url.Values{"csrf_token": {csrfCookie.Value}, "username": {"admin"}, "password": {"correct horse battery staple"}}
	loginRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.AddCookie(csrfCookie)
	loginRecorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(loginRecorder, loginRequest)
	if loginRecorder.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", loginRecorder.Code)
	}
	if loginRecorder.Header().Get("Location") != "/account/password" {
		t.Fatalf("first login location = %q, want password change", loginRecorder.Header().Get("Location"))
	}
	sessionCookie := cookieByName(loginRecorder.Result().Cookies(), "cows_session")
	if sessionCookie == nil || !sessionCookie.HttpOnly {
		t.Fatalf("invalid session cookie: %#v", sessionCookie)
	}
	passwordForm := url.Values{"csrf_token": {csrfCookie.Value}, "current_password": {"correct horse battery staple"}, "new_password": {"changed correct horse battery staple"}, "confirm_password": {"changed correct horse battery staple"}}
	passwordRequest := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(passwordForm.Encode()))
	passwordRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	passwordRequest.AddCookie(sessionCookie)
	passwordRequest.AddCookie(csrfCookie)
	passwordRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(passwordRecorder, passwordRequest)
	if passwordRecorder.Code != http.StatusSeeOther || passwordRecorder.Header().Get("Location") != "/" {
		t.Fatalf("password change response: status=%d location=%q", passwordRecorder.Code, passwordRecorder.Header().Get("Location"))
	}

	adminRequest := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	adminRequest.AddCookie(sessionCookie)
	adminRequest.AddCookie(csrfCookie)
	adminRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(adminRecorder, adminRequest)
	if adminRecorder.Code != http.StatusOK || !strings.Contains(adminRecorder.Body.String(), "Local accounts") {
		t.Fatalf("administrator page: status=%d body=%s", adminRecorder.Code, adminRecorder.Body.String())
	}

	createForm := url.Values{
		"csrf_token":   {csrfCookie.Value},
		"username":     {"student"},
		"display_name": {"Student"},
		"role":         {string(domain.RoleUser)},
		"password":     {"another correct password"},
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(createForm.Encode()))
	createRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRequest.AddCookie(sessionCookie)
	createRequest.AddCookie(csrfCookie)
	createRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusSeeOther {
		t.Fatalf("create user status = %d", createRecorder.Code)
	}

	templatesRequest := httptest.NewRequest(http.MethodGet, "/admin/templates", nil)
	templatesRequest.AddCookie(sessionCookie)
	templatesRequest.AddCookie(csrfCookie)
	templatesRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(templatesRecorder, templatesRequest)
	if templatesRecorder.Code != http.StatusOK || !strings.Contains(templatesRecorder.Body.String(), "Workspace templates") {
		t.Fatalf("template administrator page: status=%d body=%s", templatesRecorder.Code, templatesRecorder.Body.String())
	}
	templateForm := url.Values{
		"csrf_token":          {csrfCookie.Value},
		"name":                {"Research environment"},
		"description":         {"Approved research environment"},
		"image_reference":     {"registry.example/research:1"},
		"default_cpu_millis":  {"1000"},
		"max_cpu_millis":      {"4000"},
		"default_memory_mib":  {"2048"},
		"max_memory_mib":      {"8192"},
		"default_storage_gib": {"20"},
		"access_methods":      {"terminal"},
		"allowed_roles":       {"user"},
		"enabled":             {"on"},
	}
	templateRequest := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(templateForm.Encode()))
	templateRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	templateRequest.AddCookie(sessionCookie)
	templateRequest.AddCookie(csrfCookie)
	templateRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(templateRecorder, templateRequest)
	if templateRecorder.Code != http.StatusSeeOther {
		t.Fatalf("create template status = %d body=%s", templateRecorder.Code, templateRecorder.Body.String())
	}
	admin, _, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator for copy: %v", err)
	}
	templates, err := server.workspace.ListTemplates(ctx, admin.ID)
	if err != nil || len(templates) != 1 {
		t.Fatalf("load created template for copy: count=%d err=%v", len(templates), err)
	}
	copyRequest := httptest.NewRequest(http.MethodGet, "/admin/templates/"+templates[0].ID+"/copy", nil)
	copyRequest.AddCookie(sessionCookie)
	copyRequest.AddCookie(csrfCookie)
	copyRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(copyRecorder, copyRequest)
	copyBody := copyRecorder.Body.String()
	if copyRecorder.Code != http.StatusOK || !strings.Contains(copyBody, `name="name" type="text" value=""`) || !strings.Contains(copyBody, `name="image_reference" type="text" value="registry.example/research:1"`) {
		t.Fatalf("copy template form: status=%d body=%s", copyRecorder.Code, copyBody)
	}
}

func TestAdministratorRouteRedirectsUnauthenticatedUsers(t *testing.T) {
	server, _ := testServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/users", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated admin response: status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

// TestSessionRedirectNavigatesHTMXRequests covers polled and clicked htmx
// requests that arrive without a usable session. htmx follows a plain
// redirect inside its request and would swap the sign-in form into the
// fragment, so those requests must get HX-Redirect instead, while ordinary
// navigation keeps the 303.
func TestSessionRedirectNavigatesHTMXRequests(t *testing.T) {
	server, authService := testServer(t)
	if _, err := authService.BootstrapAdministrator(context.Background(), auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	// A bootstrapped administrator must change the password first.
	_, pendingToken, err := authService.Authenticate(context.Background(), "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	pendingPassword := &http.Cookie{Name: "cows_session", Value: pendingToken}
	expired := &http.Cookie{Name: "cows_session", Value: "no-longer-a-session"}
	tests := []struct {
		name     string
		method   string
		path     string
		session  *http.Cookie
		location string
	}{
		{name: "polled workspace list without session", method: http.MethodGet, path: "/fragments/workspaces", location: "/login"},
		{name: "polled detail header with expired session", method: http.MethodGet, path: "/workspaces/missing/access/header", session: expired, location: "/login"},
		{name: "polled administrator capacity with expired session", method: http.MethodGet, path: "/admin/runtime/capacity", session: expired, location: "/login"},
		{name: "workspace action without session", method: http.MethodPost, path: "/workspaces/missing/stop", location: "/login"},
		{name: "user fragment pending password change", method: http.MethodGet, path: "/fragments/workspaces", session: pendingPassword, location: "/account/password"},
		{name: "administrator fragment pending password change", method: http.MethodGet, path: "/admin/runtime/capacity", session: pendingPassword, location: "/account/password"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, htmx := range []bool{true, false} {
				request := httptest.NewRequest(test.method, test.path, nil)
				if test.session != nil {
					request.AddCookie(test.session)
				}
				if htmx {
					request.Header.Set("HX-Request", "true")
				}
				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, request)
				redirect, location := recorder.Header().Get("HX-Redirect"), recorder.Header().Get("Location")
				if htmx && (recorder.Code != http.StatusOK || redirect != test.location || location != "" || recorder.Body.Len() != 0) {
					t.Fatalf("htmx: status=%d HX-Redirect=%q Location=%q body=%q", recorder.Code, redirect, location, recorder.Body.String())
				}
				if !htmx && (recorder.Code != http.StatusSeeOther || location != test.location || redirect != "") {
					t.Fatalf("page: status=%d Location=%q HX-Redirect=%q", recorder.Code, location, redirect)
				}
			}
		})
	}
}

func TestNonAdministratorCannotOpenAdministratorUI(t *testing.T) {
	server, authService := testServer(t)
	ctx := context.Background()
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
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "student", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, userToken, err := authService.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
	}
	student, _, err := authService.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate student for password change: %v", err)
	}
	if err := authService.ChangePassword(ctx, student.ID, "another correct password", "changed student password"); err != nil {
		t.Fatalf("change student password: %v", err)
	}
	_, userToken, err = authService.Authenticate(ctx, "student", "changed student password")
	if err != nil {
		t.Fatalf("authenticate changed student password: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	request.AddCookie(&http.Cookie{Name: "cows_session", Value: userToken})
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-administrator response = %d, want 403", recorder.Code)
	}
}

func TestAdministratorCanRemoveExplicitUserQuota(t *testing.T) {
	server, authService := testServer(t)
	ctx := context.Background()
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
	student, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "quota-user", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create quota user: %v", err)
	}
	if _, err := server.quota.Set(ctx, admin.ID, student.ID, quota.Input{MaxCPUMillis: 1000}); err != nil {
		t.Fatalf("assign quota: %v", err)
	}
	_, token, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate changed administrator: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: token}
	legacyRequest := httptest.NewRequest(http.MethodGet, "/admin/quotas", nil)
	legacyRequest.AddCookie(sessionCookie)
	legacyRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(legacyRecorder, legacyRequest)
	if legacyRecorder.Code != http.StatusNotFound {
		t.Fatalf("legacy quota page response = %d, want 404", legacyRecorder.Code)
	}
	pageRequest := httptest.NewRequest(http.MethodGet, "/admin/users/"+student.ID+"/edit", nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil || !strings.Contains(pageRecorder.Body.String(), "User quota") {
		t.Fatalf("user edit page response: status=%d csrf=%#v body=%s", pageRecorder.Code, csrfCookie, pageRecorder.Body.String())
	}
	updateForm := url.Values{
		"csrf_token":             {csrfCookie.Value},
		"max_cpu_millis":         {"1500"},
		"max_memory_mib":         {"1024"},
		"max_storage_gib":        {"10"},
		"max_workspaces":         {"3"},
		"max_running_workspaces": {"1"},
	}
	updateRequest := httptest.NewRequest(http.MethodPost, "/admin/users/"+student.ID+"/quota", strings.NewReader(updateForm.Encode()))
	updateRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateRequest.AddCookie(sessionCookie)
	updateRequest.AddCookie(csrfCookie)
	updateRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(updateRecorder, updateRequest)
	if updateRecorder.Code != http.StatusSeeOther || updateRecorder.Header().Get("Location") != "/admin/users/"+student.ID+"/edit" {
		t.Fatalf("update quota response: status=%d location=%q body=%s", updateRecorder.Code, updateRecorder.Header().Get("Location"), updateRecorder.Body.String())
	}
	updated, err := server.quota.Get(ctx, admin.ID, student.ID)
	if err != nil || updated.MaxStorageBytes != 10<<30 {
		t.Fatalf("updated quota = %#v, err=%v", updated, err)
	}
	form := url.Values{"csrf_token": {csrfCookie.Value}}
	request := httptest.NewRequest(http.MethodPost, "/admin/users/"+student.ID+"/quota/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/admin/users/"+student.ID+"/edit" {
		t.Fatalf("remove quota response: status=%d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
	if _, err := server.quota.Get(ctx, admin.ID, student.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("quota after removal = %v, want not found", err)
	}
}

func TestAdministratorCanEditGroupQuotaFromGroupView(t *testing.T) {
	server, authService := testServer(t)
	ctx := context.Background()
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
	group, err := authService.CreateGroup(ctx, admin.ID, "research", "Research users")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	_, token, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate changed administrator: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: token}
	pageRequest := httptest.NewRequest(http.MethodGet, "/admin/groups/"+group.ID+"/edit", nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil || !strings.Contains(pageRecorder.Body.String(), "Group quota") {
		t.Fatalf("group edit page response: status=%d csrf=%#v body=%s", pageRecorder.Code, csrfCookie, pageRecorder.Body.String())
	}
	form := url.Values{
		"csrf_token":             {csrfCookie.Value},
		"max_cpu_millis":         {"2000"},
		"max_memory_mib":         {"2048"},
		"max_storage_gib":        {"20"},
		"max_workspaces":         {"4"},
		"max_running_workspaces": {"2"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/groups/"+group.ID+"/quota", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/admin/groups/"+group.ID+"/edit" {
		t.Fatalf("group quota response: status=%d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
	assigned, err := server.quota.GetGroup(ctx, admin.ID, group.ID)
	if err != nil || assigned.MaxRunningWorkspaces != 2 {
		t.Fatalf("assigned group quota = %#v, err=%v", assigned, err)
	}
}

func TestObservedErrorTextDoesNotExposeRuntimeDetails(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{code: "runtime_create_failed", want: "Container creation failed."},
		{code: "runtime_start_failed", want: "Container start failed."},
		{code: "runtime_missing", want: "The managed container is missing from Podman."},
		{code: "unknown_runtime_detail", want: "The runtime reported a workspace problem."},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			if got := observedErrorText(test.code); got != test.want {
				t.Fatalf("observed error text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPasswordChangeKeepsCallerAndRevokesOtherSessions(t *testing.T) {
	server, authService := testServer(t)
	ctx := context.Background()
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	_, callerToken, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate caller: %v", err)
	}
	_, stolenToken, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate stolen session: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: callerToken}
	pageRecorder := httptest.NewRecorder()
	pageRequest := httptest.NewRequest(http.MethodGet, "/account/password", nil)
	pageRequest.AddCookie(sessionCookie)
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil {
		t.Fatalf("password page: status=%d csrf=%#v", pageRecorder.Code, csrfCookie)
	}
	form := url.Values{
		"csrf_token":       {csrfCookie.Value},
		"current_password": {"correct horse battery staple"},
		"new_password":     {"changed correct horse battery staple"},
		"confirm_password": {"changed correct horse battery staple"},
	}
	changeRecorder := httptest.NewRecorder()
	changeRequest := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	changeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	changeRequest.AddCookie(sessionCookie)
	changeRequest.AddCookie(csrfCookie)
	server.Handler().ServeHTTP(changeRecorder, changeRequest)
	if changeRecorder.Code != http.StatusSeeOther || changeRecorder.Header().Get("Location") != "/" {
		t.Fatalf("change password: status=%d location=%q", changeRecorder.Code, changeRecorder.Header().Get("Location"))
	}
	callerRecorder := httptest.NewRecorder()
	callerRequest := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	callerRequest.AddCookie(sessionCookie)
	server.Handler().ServeHTTP(callerRecorder, callerRequest)
	if callerRecorder.Code != http.StatusOK {
		t.Fatalf("caller session after password change = %d, want 200", callerRecorder.Code)
	}
	stolenRecorder := httptest.NewRecorder()
	stolenRequest := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	stolenRequest.AddCookie(&http.Cookie{Name: "cows_session", Value: stolenToken})
	server.Handler().ServeHTTP(stolenRecorder, stolenRequest)
	if stolenRecorder.Code != http.StatusSeeOther || stolenRecorder.Header().Get("Location") != "/login" {
		t.Fatalf("stolen session after password change = %d location=%q, want redirect to /login", stolenRecorder.Code, stolenRecorder.Header().Get("Location"))
	}
}

func TestCSRFCookieLifetimeMatchesSessionLifetime(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	store := sqlite.New(db)
	authService, err := auth.New(store, 8*time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	server, err := New(db, authService, workspace.New(store), quota.New(store), nil, Options{SessionLifetime: 8 * time.Hour})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	csrfCookie := cookieByName(recorder.Result().Cookies(), "cows_csrf")
	if csrfCookie == nil {
		t.Fatal("login page did not set a CSRF cookie")
	}
	if want := int((8 * time.Hour).Seconds()); csrfCookie.MaxAge != want {
		t.Fatalf("CSRF cookie MaxAge = %d, want %d (session lifetime)", csrfCookie.MaxAge, want)
	}
}

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

type storageTestRuntime struct {
	*terminalRuntime
	removeVolumeErr error
	removedVolumes  []string
}

func (r *storageTestRuntime) RemoveWorkspace(context.Context, string) error      { return nil }
func (r *storageTestRuntime) VolumeExists(context.Context, string) (bool, error) { return true, nil }
func (r *storageTestRuntime) RemoveVolume(_ context.Context, name string) error {
	if r.removeVolumeErr != nil {
		return r.removeVolumeErr
	}
	r.removedVolumes = append(r.removedVolumes, name)
	return nil
}

func TestStorageVolumeDeleteKeepsTombstoneWhenRemovalFails(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
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
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "volume-owner", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, _, err := authService.Authenticate(ctx, "volume-owner", "another correct password")
	if err != nil {
		t.Fatalf("authenticate owner: %v", err)
	}
	if err := authService.ChangePassword(ctx, owner.ID, "another correct password", "changed another correct password"); err != nil {
		t.Fatalf("change owner password: %v", err)
	}
	owner, sessionToken, err := authService.Authenticate(ctx, "volume-owner", "changed another correct password")
	if err != nil {
		t.Fatalf("re-authenticate owner: %v", err)
	}

	fake := &storageTestRuntime{terminalRuntime: newTerminalRuntime()}
	service := workspace.NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Storage Volume Template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessFiles}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
		Configuration: domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "data", Type: domain.TemplateMountVolume, ContainerPath: "/data", FileManager: true}}},
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	old, err := service.CreateWorkspace(ctx, owner.ID, workspace.CreateWorkspaceInput{Name: "old volume workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.UpdateWorkspaceObservedState(ctx, old.ID, "stopped", "runtime-storage-test", "", "", old.CreatedAt, old.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete workspace to create retained volume: %v", err)
	}
	volumes, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID)
	if err != nil || len(volumes) != 1 {
		t.Fatalf("retained volumes = %d, err=%v, want 1", len(volumes), err)
	}

	server, err := New(db, authService, service, nil, fake, Options{SessionLifetime: time.Hour, Store: store})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: sessionToken}
	pageRequest := httptest.NewRequest(http.MethodGet, "/storage", nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil {
		t.Fatalf("storage page response: status=%d csrf=%#v", pageRecorder.Code, csrfCookie)
	}

	fake.removeVolumeErr = errors.New("simulated podman failure")
	deletePath := "/storage/volumes/" + old.ID + "/data/delete"
	form := url.Values{"csrf_token": {csrfCookie.Value}}
	deleteRequest := httptest.NewRequest(http.MethodPost, deletePath, strings.NewReader(form.Encode()))
	deleteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteRequest.AddCookie(sessionCookie)
	deleteRequest.AddCookie(csrfCookie)
	deleteRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete-with-failing-removal status = %d, want 503, body = %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}

	volumesAfter, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID)
	if err != nil || len(volumesAfter) != 1 {
		t.Fatalf("retained volumes after failed removal = %d, err=%v, want 1 (tombstone must survive)", len(volumesAfter), err)
	}
}

func TestAdminDirectoriesRecoversStorageAfterOwnerDeleted(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	store := sqlite.New(db)
	authService, err := auth.New(store, time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, adminToken, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := authService.ChangePassword(ctx, admin.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	target, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "orphan-owner", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create target user: %v", err)
	}
	if err := authService.ChangePassword(ctx, target.ID, "another correct password", "changed another correct password"); err != nil {
		t.Fatalf("change target password: %v", err)
	}

	fake := &storageTestRuntime{terminalRuntime: newTerminalRuntime()}
	mountRoot, archiveRoot := t.TempDir(), t.TempDir()
	service := workspace.NewWithRuntimeAndMountRoots(store, fake, mountRoot, archiveRoot)
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Admin Directory Recovery Template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessFiles}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
		Configuration: domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "designs", Type: domain.TemplateMountDirectory, ContainerPath: "/designs", FileManager: true}}},
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	old, err := service.CreateWorkspace(ctx, target.ID, workspace.CreateWorkspaceInput{Name: "orphaned directory workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, target.ID, old.ID); err != nil {
		t.Fatalf("delete workspace to create retained directory: %v", err)
	}
	if err := authService.SetUserDisabled(ctx, admin.ID, target.ID, true); err != nil {
		t.Fatalf("disable target: %v", err)
	}
	if err := authService.DeleteUser(ctx, admin.ID, target.ID); err != nil {
		t.Fatalf("delete target (must succeed: they no longer own any workspace rows): %v", err)
	}

	server, err := New(db, authService, service, nil, fake, Options{SessionLifetime: time.Hour, Store: store})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: adminToken}
	pageRequest := httptest.NewRequest(http.MethodGet, "/admin/directories", nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	if pageRecorder.Code != http.StatusOK || !strings.Contains(pageRecorder.Body.String(), "orphaned directory workspace") {
		t.Fatalf("admin directories page status=%d body=%s", pageRecorder.Code, pageRecorder.Body.String())
	}
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if csrfCookie == nil {
		t.Fatalf("expected a csrf cookie")
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/admin/directories/"+old.ID+"/download.zip", nil)
	downloadRequest.AddCookie(sessionCookie)
	downloadRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(downloadRecorder, downloadRequest)
	if downloadRecorder.Code != http.StatusOK || downloadRecorder.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download status=%d content-type=%q", downloadRecorder.Code, downloadRecorder.Header().Get("Content-Type"))
	}

	form := url.Values{"csrf_token": {csrfCookie.Value}}
	deleteRequest := httptest.NewRequest(http.MethodPost, "/admin/directories/"+old.ID+"/delete", strings.NewReader(form.Encode()))
	deleteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteRequest.AddCookie(sessionCookie)
	deleteRequest.AddCookie(csrfCookie)
	deleteRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusSeeOther {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if directories, err := store.ListAllRetainedWorkspaceDirectories(ctx); err != nil || len(directories) != 0 {
		t.Fatalf("retained directories after admin delete = %d, err=%v, want 0", len(directories), err)
	}
}

// testServerWithOptions mirrors testServer but lets a test choose the flags
// that gate the credential-link routes.
func testServerWithOptions(t *testing.T, options Options) *Server {
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
	options.SessionLifetime = time.Hour
	server, err := New(db, authService, workspace.New(store), quota.New(store), nil, options)
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	return server
}

func TestInvitationRoutesAreHiddenUntilInvitationsAreEnabled(t *testing.T) {
	disabled := testServerWithOptions(t, Options{InvitationsEnabled: false})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		recorder := httptest.NewRecorder()
		disabled.Handler().ServeHTTP(recorder, httptest.NewRequest(method, "/invitation/accept?token=abc", nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s /invitation/accept with invitations disabled = %d, want 404", method, recorder.Code)
		}
	}

	enabled := testServerWithOptions(t, Options{InvitationsEnabled: true})
	recorder := httptest.NewRecorder()
	enabled.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/invitation/accept?token=abc", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /invitation/accept with invitations enabled = %d, want 200", recorder.Code)
	}
}

func TestInvitationAcceptRequiresCSRF(t *testing.T) {
	server := testServerWithOptions(t, Options{InvitationsEnabled: true})
	request := httptest.NewRequest(http.MethodPost, "/invitation/accept", strings.NewReader("token=abc&new_password=chosen-password-1&confirm_password=chosen-password-1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /invitation/accept without a CSRF token = %d, want 403", recorder.Code)
	}
}

func TestLoginPageLinksPasswordResetOnlyWhenItIsUsable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		server := testServerWithOptions(t, Options{PasswordResetEnabled: enabled})
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /login = %d", recorder.Code)
		}
		linked := strings.Contains(recorder.Body.String(), `href="/password/reset"`)
		if linked != enabled {
			t.Fatalf("password reset link present = %v, want %v", linked, enabled)
		}
	}
}

func TestAdministratorEditsAccountEmailAndPassword(t *testing.T) {
	server, authService := testServer(t)
	ctx := context.Background()
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: "admin", Email: "admin@example.test", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, _, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := authService.ChangePassword(ctx, admin.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	student, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "student", Email: "typo@example.test", DisplayName: "Student", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create student: %v", err)
	}
	_, token, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate changed administrator: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: token}
	editURL := "/admin/users/" + student.ID + "/edit"
	pageRequest := httptest.NewRequest(http.MethodGet, editURL, nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil || !strings.Contains(pageRecorder.Body.String(), "Account details") {
		t.Fatalf("user edit page response: status=%d csrf=%#v body=%s", pageRecorder.Code, csrfCookie, pageRecorder.Body.String())
	}

	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf_token", csrfCookie.Value)
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(sessionCookie)
		request.AddCookie(csrfCookie)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	profile := post("/admin/users/"+student.ID+"/profile", url.Values{"email": {"student@example.test"}, "display_name": {"Real Student"}})
	if profile.Code != http.StatusSeeOther || profile.Header().Get("Location") != editURL+"?notice=profile" {
		t.Fatalf("profile update response: status=%d location=%q", profile.Code, profile.Header().Get("Location"))
	}
	updated, err := authService.FindUserForAdmin(ctx, admin.ID, student.ID)
	if err != nil || updated.Email != "student@example.test" || updated.DisplayName != "Real Student" {
		t.Fatalf("updated user = %+v %v", updated, err)
	}
	// A rejected change re-renders the editor rather than redirecting, so the
	// submitted values survive and the control at fault is marked.
	invalid := post("/admin/users/"+student.ID+"/profile", url.Values{"email": {"not an address"}, "display_name": {"Real Student"}})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid profile response: status=%d location=%q", invalid.Code, invalid.Header().Get("Location"))
	}
	if body := invalid.Body.String(); !strings.Contains(body, `id="email" name="email" type="email" value="not an address" maxlength="254" autocomplete="email" aria-invalid="true"`) {
		t.Fatalf("rejected email field is not marked: %s", body)
	}

	// An administrator's own account offers no password form here: setting it
	// would end the session making the request.
	selfRequest := httptest.NewRequest(http.MethodGet, "/admin/users/"+admin.ID+"/edit", nil)
	selfRequest.AddCookie(sessionCookie)
	selfRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(selfRecorder, selfRequest)
	selfPasswordAction := "action=\"/admin/users/" + admin.ID + "/password\""
	if body := selfRecorder.Body.String(); strings.Contains(body, selfPasswordAction) || !strings.Contains(body, "Account security") {
		t.Fatalf("own account edit page offers a password form: %s", body)
	}
	selfPost := post("/admin/users/"+admin.ID+"/password", url.Values{"new_password": {"a password for myself"}, "confirm_password": {"a password for myself"}})
	if selfPost.Header().Get("Location") != "/admin/users/"+admin.ID+"/edit?error=password_self" {
		t.Fatalf("self password set location=%q", selfPost.Header().Get("Location"))
	}
	if _, _, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple"); err != nil {
		t.Fatalf("administrator password changed by a refused self set: %v", err)
	}

	// Two accounts must not share an address: the reset endpoint resolves an
	// address to exactly one account.
	taken := post("/admin/users/"+student.ID+"/profile", url.Values{"email": {"admin@example.test"}, "display_name": {"Real Student"}})
	if taken.Code != http.StatusBadRequest {
		t.Fatalf("duplicate address status=%d location=%q", taken.Code, taken.Header().Get("Location"))
	}
	if body := taken.Body.String(); !strings.Contains(body, "already belongs to another account") {
		t.Fatalf("duplicate address is not reported on the email field: %s", body)
	}

	mismatch := post("/admin/users/"+student.ID+"/password", url.Values{"new_password": {"an administrator chosen password"}, "confirm_password": {"a different password entirely"}})
	if mismatch.Code != http.StatusSeeOther || mismatch.Header().Get("Location") != editURL+"?error=password_mismatch" {
		t.Fatalf("mismatched password response: status=%d location=%q", mismatch.Code, mismatch.Header().Get("Location"))
	}
	if _, _, err := authService.Authenticate(ctx, "student", "another correct password"); err != nil {
		t.Fatalf("password changed despite the mismatch: %v", err)
	}
	changed := post("/admin/users/"+student.ID+"/password", url.Values{"new_password": {"an administrator chosen password"}, "confirm_password": {"an administrator chosen password"}})
	if changed.Code != http.StatusSeeOther || changed.Header().Get("Location") != editURL+"?notice=password" {
		t.Fatalf("password response: status=%d location=%q", changed.Code, changed.Header().Get("Location"))
	}
	if _, _, err := authService.Authenticate(ctx, "student", "an administrator chosen password"); err != nil {
		t.Fatalf("authenticate with the new password: %v", err)
	}

	// A signed-in user may not edit anyone, including themselves, through the
	// administrator routes. The student clears the forced password change
	// first, so the refusal below is about their role and not that flag.
	signedIn, studentToken, err := authService.Authenticate(ctx, "student", "an administrator chosen password")
	if err != nil {
		t.Fatalf("authenticate student: %v", err)
	}
	if !signedIn.MustChangePassword {
		t.Fatal("a password set by an administrator must be changed at next sign-in")
	}
	if err := authService.ChangePassword(ctx, student.ID, "an administrator chosen password", "a password of their own"); err != nil {
		t.Fatalf("student changes their password: %v", err)
	}
	for _, path := range []string{"/profile", "/password"} {
		request := httptest.NewRequest(http.MethodPost, "/admin/users/"+student.ID+path, strings.NewReader("email=self@example.test&new_password=a password of their own&confirm_password=a password of their own&csrf_token="+csrfCookie.Value))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: "cows_session", Value: studentToken})
		request.AddCookie(csrfCookie)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("non-administrator POST %s = %d, want 403", path, recorder.Code)
		}
	}
}

// adminSession bootstraps an administrator, clears the forced password change,
// and returns the cookies an authenticated form post needs.
func adminSession(t *testing.T, server *Server, authService *auth.Service) (*http.Cookie, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: "admin", Email: "admin@example.test", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, _, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := authService.ChangePassword(ctx, admin.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	_, token, err := authService.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate changed administrator: %v", err)
	}
	session := &http.Cookie{Name: "cows_session", Value: token}
	pageRequest := httptest.NewRequest(http.MethodGet, "/admin/templates/new", nil)
	pageRequest.AddCookie(session)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrf := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrf == nil {
		t.Fatalf("template form page: status=%d csrf=%#v", pageRecorder.Code, csrf)
	}
	return session, csrf
}

// TestTemplateFormMarksEveryRejectedControl covers the point of attributing
// validation failures to fields: the long template form has to say which of
// its inputs is wrong, and it has to say so for all of them at once rather
// than revealing one problem per submission.
func TestTemplateFormMarksEveryRejectedControl(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)

	form := url.Values{
		"csrf_token": {csrf.Value},
		// Configurable, so the maximum controls are live and their rules apply.
		"resources_configurable":   {"on"},
		"name":                     {""},
		"image_reference":          {"registry.example/has a space:1"},
		"image_digest":             {"sha256:nothex"},
		"default_cpu_millis":       {"4000"},
		"max_cpu_millis":           {"1000"},
		"default_memory_mib":       {"2048"},
		"max_memory_mib":           {"not a number"},
		"initial_connection_hours": {"24"},
		"stopped_retention_hours":  {"24"},
		"configuration_json":       {"{\"mounts\": ["},
		"access_methods":           {"terminal"},
		"allowed_roles":            {"user"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	request.AddCookie(csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("rejected template status = %d, want 400", recorder.Code)
	}
	body := recorder.Body.String()
	for _, field := range []string{"name", "image_reference", "image_digest", "max_cpu_millis", "max_memory_mib", "configuration_json"} {
		if !strings.Contains(body, `aria-describedby="field-error-`+field+`"`) {
			t.Fatalf("control %q is not marked invalid: %s", field, body)
		}
		if !strings.Contains(body, `id="field-error-`+field+`"`) {
			t.Fatalf("control %q has no message: %s", field, body)
		}
	}
	// The alert points at the marks instead of repeating them, so the two can
	// never describe the problem differently.
	if !strings.Contains(body, "fields need attention. They are marked below.") {
		t.Fatalf("summary alert does not point at the marks: %s", body)
	}
	// Accepted values survive the round trip, so only the marked controls
	// have to be fixed.
	if !strings.Contains(body, `name="default_memory_mib" type="number" min="1" value="2048"`) {
		t.Fatalf("accepted value was not preserved: %s", body)
	}
}

// TestTemplateFormMarksNameConflict covers the attribution the service cannot
// make on its own, because it is about the stored templates rather than the
// submitted value.
func TestTemplateFormMarksNameConflict(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)

	form := url.Values{
		"csrf_token":               {csrf.Value},
		"name":                     {"Research environment"},
		"image_reference":          {"registry.example/research:1"},
		"default_cpu_millis":       {"1000"},
		"max_cpu_millis":           {"4000"},
		"default_memory_mib":       {"2048"},
		"max_memory_mib":           {"8192"},
		"initial_connection_hours": {"24"},
		"stopped_retention_hours":  {"24"},
		"configuration_json":       {"{}"},
		"access_methods":           {"terminal"},
		"allowed_roles":            {"user"},
	}
	post := func() *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(session)
		request.AddCookie(csrf)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if first := post(); first.Code != http.StatusSeeOther {
		t.Fatalf("first create status = %d body=%s", first.Code, first.Body.String())
	}
	second := post()
	if second.Code != http.StatusBadRequest {
		t.Fatalf("duplicate name status = %d, want 400", second.Code)
	}
	body := second.Body.String()
	if !strings.Contains(body, `aria-describedby="field-error-name"`) || !strings.Contains(body, "already exists") {
		t.Fatalf("duplicate name is not marked on the name control: %s", body)
	}
}

// TestFileManagerWithoutAMountMarksBothControls covers a rule that spans two
// inputs: either the access method goes or the mount arrives, so the form
// marks both rather than picking one.
func TestFileManagerWithoutAMountMarksBothControls(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)

	form := url.Values{
		"csrf_token":               {csrf.Value},
		"name":                     {"Files without a mount"},
		"image_reference":          {"registry.example/research:1"},
		"default_cpu_millis":       {"1000"},
		"max_cpu_millis":           {"4000"},
		"default_memory_mib":       {"2048"},
		"max_memory_mib":           {"8192"},
		"initial_connection_hours": {"24"},
		"stopped_retention_hours":  {"24"},
		"configuration_json":       {"{}"},
		"access_methods":           {"files"},
		"allowed_roles":            {"user"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	request.AddCookie(csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	body := recorder.Body.String()
	for _, field := range []string{"access_methods", "configuration_json"} {
		if !strings.Contains(body, `id="field-error-`+field+`"`) {
			t.Fatalf("control %q is not marked: %s", field, body)
		}
	}
}

// TestHostSettingsMarkTheOutOfRangeFactor checks that a form outside the
// template editor gets the same treatment.
func TestHostSettingsMarkTheOutOfRangeFactor(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)

	form := url.Values{
		"csrf_token":                {csrf.Value},
		"host_storage_gib":          {"100"},
		"cpu_overbooking_factor":    {"1"},
		"memory_overbooking_factor": {"50000"},
		"reserved_storage_gib":      {"200"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	request.AddCookie(csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `id="field-error-memory_overbooking_factor"`) {
		t.Fatalf("the out-of-range factor is not marked: %s", body)
	}
	if !strings.Contains(body, `id="field-error-reserved_storage_gib"`) {
		t.Fatalf("reserved storage above the host total is not marked: %s", body)
	}
	if strings.Contains(body, `id="field-error-cpu_overbooking_factor"`) {
		t.Fatalf("the accepted CPU factor was marked: %s", body)
	}
}

// TestFixedResourceTemplateAcceptsADefaultAboveTheFrozenMaximum is the
// regression for a dead end in the template editor: with user-selectable
// resources switched off the maximum controls are readonly, so rejecting a
// default raised above them complained about a control the administrator could
// not change.
func TestFixedResourceTemplateAcceptsADefaultAboveTheFrozenMaximum(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)

	form := url.Values{
		"csrf_token": {csrf.Value},
		// resources_configurable deliberately absent: the checkbox is off, so
		// the maximum fields below are the frozen ones the form submits.
		"name":                     {"Fixed eight cores"},
		"image_reference":          {"registry.example/research:1"},
		"default_cpu_millis":       {"8000"},
		"max_cpu_millis":           {"4000"},
		"default_memory_mib":       {"16384"},
		"max_memory_mib":           {"8192"},
		"initial_connection_hours": {"24"},
		"stopped_retention_hours":  {"24"},
		"configuration_json":       {"{}"},
		"access_methods":           {"terminal"},
		"allowed_roles":            {"user"},
		"enabled":                  {"on"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/templates", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	request.AddCookie(csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("fixed-resource template status = %d, want 303: %s", recorder.Code, recorder.Body.String())
	}
	admin, _, err := authService.Authenticate(context.Background(), "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	templates, err := server.workspace.ListTemplates(context.Background(), admin.ID)
	if err != nil || len(templates) != 1 {
		t.Fatalf("load created template: count=%d err=%v", len(templates), err)
	}
	stored := templates[0]
	if stored.MaxCPUMillis != 8000 || stored.MaxMemoryBytes != 16384<<20 {
		t.Fatalf("stored maximums = %d mCPU / %d bytes, want them following the defaults", stored.MaxCPUMillis, stored.MaxMemoryBytes)
	}
}
