package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/repository/sqlite"
)

func TestCSRFRejectsInjectedAndOtherSessionCookies(t *testing.T) {
	server, _ := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "https://cows.example.test/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "first-session"})
	recorder := httptest.NewRecorder()
	token := server.ensureCSRF(recorder, request)
	cookie := cookieByName(recorder.Result().Cookies(), csrfCookieName)
	if cookie == nil || !cookie.HttpOnly {
		t.Fatal("CSRF cookie must be HttpOnly")
	}
	for _, test := range []struct {
		name, token, session, origin, site string
		valid                              bool
	}{
		{"same session", token, "first-session", "https://cows.example.test", "same-origin", true},
		{"no browser metadata", token, "first-session", "", "", true},
		{"injected cookie", "attacker-token", "first-session", "", "", false},
		{"other session", token, "second-session", "", "", false},
		{"anonymous cookie", token, "", "", "", false},
		{"sibling origin", token, "first-session", "https://evil.example.test", "same-site", false},
		{"cross-site origin", token, "first-session", "https://evil.test", "cross-site", false},
		{"cross-site metadata", token, "first-session", "https://cows.example.test", "cross-site", false},
		{"missing same-site origin", token, "first-session", "", "same-site", false},
		{"opaque origin", token, "first-session", "null", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{"csrf_token": {test.token}}
			r := httptest.NewRequest(http.MethodPost, "https://cows.example.test/action", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Sec-Fetch-Site", test.site)
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: test.token})
			if test.session != "" {
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: test.session})
			}
			if got := server.validCSRF(r); got != test.valid {
				t.Fatalf("validCSRF=%t want %t", got, test.valid)
			}
		})
	}
	replacement := httptest.NewRecorder()
	request.Header.Set("Cookie", "")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "second-session"})
	request.AddCookie(cookie)
	if fresh := server.ensureCSRF(replacement, request); fresh == token || replacement.Header().Get("Set-Cookie") == "" {
		t.Fatal("session change did not replace CSRF token")
	}
}

func postCredentialForm(server *Server, route string, form url.Values, csrf *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, route, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(csrf)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	return w
}

func TestPublicCredentialConfirmationsShareBoundedAttemptBudget(t *testing.T) {
	server, _ := testServer(t)
	server.options.PasswordResetEnabled = true
	server.options.InvitationsEnabled = true
	server.options.LoginLimiter, _ = auth.NewLoginLimiter(1, time.Hour)
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login", nil))
	csrf := cookieByName(page.Result().Cookies(), csrfCookieName)
	form := url.Values{"csrf_token": {csrf.Value}, "token": {"invalid-token"}, "new_password": {"correct length password"}, "confirm_password": {"correct length password"}}
	if w := postCredentialForm(server, "/password/reset/confirm", form, csrf); w.Code != http.StatusBadRequest {
		t.Fatalf("first confirmation status=%d", w.Code)
	}
	if w := postCredentialForm(server, "/invitation/accept", form, csrf); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("second confirmation status=%d", w.Code)
	}
}

func TestSuccessfulRegistrationConsumesAttemptBudget(t *testing.T) {
	server, _ := testServer(t)
	service, err := auth.New(sqlite.New(server.db), time.Hour, auth.RegistrationPolicy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server.auth = service
	server.options.RegistrationEnabled = true
	server.options.RegistrationLimiter, _ = auth.NewLoginLimiter(1, time.Hour)
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/register", nil))
	csrf := cookieByName(page.Result().Cookies(), csrfCookieName)
	form := url.Values{"csrf_token": {csrf.Value}, "username": {"registered-user"}, "email": {"registered@example.test"}, "password": {"correct length password"}, "confirm_password": {"correct length password"}}
	if w := postCredentialForm(server, "/register", form, csrf); w.Code != http.StatusOK {
		t.Fatalf("first registration status=%d body=%s", w.Code, w.Body)
	}
	form.Set("username", "another-user")
	form.Set("email", "another@example.test")
	if w := postCredentialForm(server, "/register", form, csrf); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second registration status=%d", w.Code)
	}
	if _, _, err := service.Authenticate(context.Background(), "another-user", "correct length password"); err == nil {
		t.Fatal("rate-limited registration created account")
	}
}

type countingRequestBody struct {
	io.ReadCloser
	read int
}

func (b *countingRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += n
	return n, err
}

func TestLifecycleBodyLimitAppliesBeforeMultipartParsing(t *testing.T) {
	server, service := testServer(t)
	session, csrf := adminSession(t, server, service)
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	if err := writer.WriteField("csrf_token", csrf.Value); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("ignored-file", "large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(bytes.Repeat([]byte("x"), 2<<20)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/workspaces/nonexistent/start", &payload)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.AddCookie(session)
	r.AddCookie(csrf)
	counted := &countingRequestBody{ReadCloser: r.Body}
	r.Body = counted
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversize status=%d", w.Code)
	}
	if counted.read > 1<<20+1 {
		t.Fatalf("body read %d bytes before cap", counted.read)
	}
}
