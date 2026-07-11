package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	webassets "github.com/cows-project/cows/web"
)

const (
	sessionCookieName = "cows_session"
	csrfCookieName    = "cows_csrf"
)

type Options struct {
	CookieSecure    bool
	SessionLifetime time.Duration
	LoginLimiter    *auth.LoginLimiter
}

type Server struct {
	db        *sql.DB
	auth      *auth.Service
	options   Options
	templates *template.Template
	static    fs.FS
}

type healthSnapshot struct {
	Status    string
	Database  string
	CheckedAt string
}

type userFormData struct {
	Username    string
	DisplayName string
	Role        string
	Error       string
}

type pageData struct {
	Title     string
	User      *domain.User
	Health    healthSnapshot
	CSRFToken string
	Error     string
	Users     []domain.User
	Form      userFormData
}

func New(db *sql.DB, authService *auth.Service, options Options) (*Server, error) {
	if options.SessionLifetime <= 0 {
		options.SessionLifetime = 8 * time.Hour
	}
	if options.LoginLimiter == nil {
		options.LoginLimiter, _ = auth.NewLoginLimiter(auth.DefaultLoginFailureLimit, auth.DefaultLoginFailureWindow)
	}
	templates, err := template.ParseFS(webassets.Files, "templates/layouts/*.html", "templates/pages/*.html", "templates/fragments/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	static, err := fs.Sub(webassets.Files, "static")
	if err != nil {
		return nil, fmt.Errorf("open static assets: %w", err)
	}
	return &Server{db: db, auth: authService, options: options, templates: templates, static: static}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /fragments/health", s.healthFragment)
	mux.HandleFunc("GET /login", s.loginGet)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("POST /logout", s.logoutPost)
	mux.HandleFunc("GET /admin/users", s.adminUsers)
	mux.HandleFunc("GET /admin/users/new", s.adminUsersNew)
	mux.HandleFunc("POST /admin/users", s.adminUsersCreate)
	mux.HandleFunc("POST /admin/users/{id}/disabled", s.adminUserDisabled)
	mux.HandleFunc("GET /", s.home)
	return mux
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	user, err := s.currentUser(r)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "home-page", pageData{Title: "COWS", User: user, CSRFToken: s.ensureCSRF(w, r), Health: s.snapshot(r.Context())})
}

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	user, err := s.currentUser(r)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return
	}
	if user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "login-page", pageData{Title: "Sign in | COWS", CSRFToken: s.ensureCSRF(w, r)})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	key := loginRateLimitKey(r)
	if !s.options.LoginLimiter.Allow(key) {
		retryAfter := int(s.options.LoginLimiter.RetryAfter(key).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		s.render(w, http.StatusTooManyRequests, "login-page", pageData{
			Title:     "Sign in | COWS",
			CSRFToken: s.ensureCSRF(w, r),
			Error:     "Too many sign-in attempts. Try again later.",
		})
		return
	}
	_, token, err := s.auth.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		s.options.LoginLimiter.Failure(key)
		s.render(w, http.StatusUnauthorized, "login-page", pageData{
			Title:     "Sign in | COWS",
			CSRFToken: s.ensureCSRF(w, r),
			Error:     "Invalid username or password.",
		})
		return
	}
	s.options.LoginLimiter.Success(key)
	s.setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logoutPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.auth.Logout(r.Context(), cookie.Value); err != nil {
			http.Error(w, "failed to sign out", http.StatusInternalServerError)
			return
		}
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	users, err := s.auth.ListUsers(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "admin-users-page", pageData{
		Title:     "Users | COWS",
		User:      &user,
		Users:     users,
		CSRFToken: s.ensureCSRF(w, r),
	})
}

func (s *Server) adminUsersNew(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "admin-users-new-page", pageData{
		Title:     "Create user | COWS",
		User:      &user,
		CSRFToken: s.ensureCSRF(w, r),
		Form:      userFormData{Role: string(domain.RoleUser)},
	})
}

func (s *Server) adminUsersCreate(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	form := userFormData{Username: r.FormValue("username"), DisplayName: r.FormValue("display_name"), Role: r.FormValue("role")}
	_, err := s.auth.CreateUser(r.Context(), user.ID, auth.CreateUserInput{
		Username:    form.Username,
		DisplayName: form.DisplayName,
		Password:    r.FormValue("password"),
		Role:        domain.Role(form.Role),
	})
	if err != nil {
		form.Error = userFormError(err)
		s.render(w, http.StatusBadRequest, "admin-users-new-page", pageData{Title: "Create user | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Form: form})
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) adminUserDisabled(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	disabled, err := strconv.ParseBool(r.FormValue("disabled"))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.auth.SetUserDisabled(r.Context(), user.ID, r.PathValue("id"), disabled); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, userActionError(err), status)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) requireAdministrator(w http.ResponseWriter, r *http.Request) (domain.User, bool) {
	user, err := s.currentUser(r)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return domain.User{}, false
	}
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return domain.User{}, false
	}
	if !user.IsAdministrator() {
		http.Error(w, "administrator permission required", http.StatusForbidden)
		return domain.User{}, false
	}
	return *user, true
}

func (s *Server) currentUser(r *http.Request) (*domain.User, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return nil, nil
		}
		return nil, err
	}
	user, err := s.auth.UserForSession(r.Context(), cookie.Value)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", MaxAge: 3600, Secure: s.options.CookieSecure, SameSite: http.SameSiteLaxMode})
	return token
}

func (s *Server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	provided := r.FormValue("csrf_token")
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(provided)) == 1
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	maxAge := int(s.options.SessionLifetime.Seconds())
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: s.options.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.options.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		return
	}
}

func userFormError(err error) string {
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		return "Use a valid username, a display name, a supported role, and a password between 12 and 72 characters."
	case errors.Is(err, repository.ErrConflict):
		return "A user with that username already exists."
	default:
		return "The user could not be created."
	}
}

func userActionError(err error) string {
	switch {
	case errors.Is(err, auth.ErrSelfDisable):
		return "You cannot disable your own administrator account."
	case errors.Is(err, auth.ErrLastAdministrator):
		return "The last active administrator cannot be disabled."
	default:
		return "The user state could not be changed."
	}
}

func (s *Server) healthFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "health-status", s.snapshot(r.Context())); err != nil {
		http.Error(w, "failed to render health status", http.StatusInternalServerError)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	snapshot := s.snapshot(r.Context())
	status := http.StatusOK
	if snapshot.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}{Status: snapshot.Status, Database: snapshot.Database})
}

func (s *Server) snapshot(ctx context.Context) healthSnapshot {
	snapshot := healthSnapshot{Status: "ok", Database: "ok", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := s.db.PingContext(ctx); err != nil {
		snapshot.Status = "degraded"
		snapshot.Database = "unavailable"
	}
	return snapshot
}

func loginRateLimitKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr == "" {
		return "unknown"
	}
	return r.RemoteAddr
}
