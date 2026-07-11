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
	server, err := New(db, authService, Options{SessionLifetime: time.Hour})
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
	sessionCookie := cookieByName(loginRecorder.Result().Cookies(), "cows_session")
	if sessionCookie == nil || !sessionCookie.HttpOnly {
		t.Fatalf("invalid session cookie: %#v", sessionCookie)
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
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "student", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, userToken, err := authService.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate user: %v", err)
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
