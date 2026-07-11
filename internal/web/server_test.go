package web

import (
	"context"
	"encoding/json"
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
	authService, err := auth.New(sqlite.New(db), time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	templateService := workspace.New(sqlite.New(db))
	server, err := New(db, authService, templateService, Options{SessionLifetime: time.Hour})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	return server, authService
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

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
