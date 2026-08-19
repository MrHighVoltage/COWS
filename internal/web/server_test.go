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

	// Delete now succeeds (workspace is stopped): the response is empty so
	// htmx removes the row, and the workspace is actually gone.
	finalDeleteRecorder := postRowAction("delete")
	if finalDeleteRecorder.Code != http.StatusOK {
		t.Fatalf("final delete status = %d, want 200", finalDeleteRecorder.Code)
	}
	if body := finalDeleteRecorder.Body.String(); body != "" {
		t.Fatalf("final delete response should be empty (row removed), got %q", body)
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
