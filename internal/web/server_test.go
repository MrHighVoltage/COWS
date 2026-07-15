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
	pageRequest := httptest.NewRequest(http.MethodGet, "/admin/quotas", nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil {
		t.Fatalf("quota page response: status=%d csrf=%#v", pageRecorder.Code, csrfCookie)
	}
	form := url.Values{"csrf_token": {csrfCookie.Value}}
	request := httptest.NewRequest(http.MethodPost, "/admin/quotas/user/"+student.ID+"/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/admin/quotas" {
		t.Fatalf("remove quota response: status=%d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
	if _, err := server.quota.Get(ctx, admin.ID, student.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("quota after removal = %v, want not found", err)
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

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
