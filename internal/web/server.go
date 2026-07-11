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
	"strings"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/workspace"
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
	workspace *workspace.Service
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

type templateFormData struct {
	ID                string
	Editing           bool
	Name              string
	Description       string
	ImageReference    string
	ImageDigest       string
	DefaultCPUMillis  string
	MaxCPUMillis      string
	DefaultMemoryMiB  string
	MaxMemoryMiB      string
	DefaultStorageGiB string
	TerminalAccess    bool
	DesktopAccess     bool
	WebAccess         bool
	UserRole          bool
	AdministratorRole bool
	Enabled           bool
	Error             string
}

type pageData struct {
	Title     string
	User      *domain.User
	Health    healthSnapshot
	CSRFToken string
	Error     string
	Users     []domain.User
	Form      userFormData
	Templates []domain.WorkspaceTemplate
	Template  templateFormData
}

func New(db *sql.DB, authService *auth.Service, templateService *workspace.Service, options Options) (*Server, error) {
	if options.SessionLifetime <= 0 {
		options.SessionLifetime = 8 * time.Hour
	}
	if options.LoginLimiter == nil {
		options.LoginLimiter, _ = auth.NewLoginLimiter(auth.DefaultLoginFailureLimit, auth.DefaultLoginFailureWindow)
	}
	templates, err := template.New("cows").Funcs(template.FuncMap{
		"mib": func(bytes int64) int64 { return bytes / (1 << 20) },
	}).ParseFS(webassets.Files, "templates/layouts/*.html", "templates/pages/*.html", "templates/fragments/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	static, err := fs.Sub(webassets.Files, "static")
	if err != nil {
		return nil, fmt.Errorf("open static assets: %w", err)
	}
	return &Server{db: db, auth: authService, workspace: templateService, options: options, templates: templates, static: static}, nil
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
	mux.HandleFunc("GET /admin/templates", s.adminTemplates)
	mux.HandleFunc("GET /admin/templates/new", s.adminTemplatesNew)
	mux.HandleFunc("POST /admin/templates", s.adminTemplatesCreate)
	mux.HandleFunc("GET /admin/templates/{id}/edit", s.adminTemplatesEdit)
	mux.HandleFunc("POST /admin/templates/{id}", s.adminTemplatesUpdate)
	mux.HandleFunc("POST /admin/templates/{id}/enabled", s.adminTemplateEnabled)
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

func (s *Server) adminTemplates(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	templates, err := s.workspace.ListTemplates(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "failed to load workspace templates", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "admin-templates-page", pageData{Title: "Templates | COWS", User: &user, Templates: templates, CSRFToken: s.ensureCSRF(w, r)})
}

func (s *Server) adminTemplatesNew(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "admin-template-form-page", pageData{
		Title:     "Create template | COWS",
		User:      &user,
		CSRFToken: s.ensureCSRF(w, r),
		Template:  defaultTemplateForm(),
	})
}

func (s *Server) adminTemplatesCreate(w http.ResponseWriter, r *http.Request) {
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
	form, input, err := s.parseTemplateForm(r)
	if err != nil {
		form.Error = templateFormError(err)
		s.render(w, http.StatusBadRequest, "admin-template-form-page", pageData{Title: "Create template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form})
		return
	}
	if _, err := s.workspace.CreateTemplate(r.Context(), user.ID, input); err != nil {
		form.Error = templateFormError(err)
		s.render(w, http.StatusBadRequest, "admin-template-form-page", pageData{Title: "Create template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form})
		return
	}
	http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
}

func (s *Server) adminTemplatesEdit(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	template, err := s.workspace.GetTemplate(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace template", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "admin-template-form-page", pageData{Title: "Edit template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: templateFormFromDomain(template)})
}

func (s *Server) adminTemplatesUpdate(w http.ResponseWriter, r *http.Request) {
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
	form, input, err := s.parseTemplateForm(r)
	form.ID = r.PathValue("id")
	form.Editing = true
	if err != nil {
		form.Error = templateFormError(err)
		s.render(w, http.StatusBadRequest, "admin-template-form-page", pageData{Title: "Edit template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form})
		return
	}
	if _, err := s.workspace.UpdateTemplate(r.Context(), user.ID, r.PathValue("id"), input); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		form.Error = templateFormError(err)
		s.render(w, status, "admin-template-form-page", pageData{Title: "Edit template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form})
		return
	}
	http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
}

func (s *Server) adminTemplateEnabled(w http.ResponseWriter, r *http.Request) {
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
	enabled, err := strconv.ParseBool(r.FormValue("enabled"))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.workspace.SetTemplateEnabled(r.Context(), user.ID, r.PathValue("id"), enabled); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, templateActionError(err), status)
		return
	}
	http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
}

func (s *Server) parseTemplateForm(r *http.Request) (templateFormData, workspace.TemplateInput, error) {
	form := templateFormData{
		Name:              r.FormValue("name"),
		Description:       r.FormValue("description"),
		ImageReference:    r.FormValue("image_reference"),
		ImageDigest:       r.FormValue("image_digest"),
		DefaultCPUMillis:  r.FormValue("default_cpu_millis"),
		MaxCPUMillis:      r.FormValue("max_cpu_millis"),
		DefaultMemoryMiB:  r.FormValue("default_memory_mib"),
		MaxMemoryMiB:      r.FormValue("max_memory_mib"),
		DefaultStorageGiB: r.FormValue("default_storage_gib"),
		Enabled:           r.FormValue("enabled") == "on",
	}
	for _, method := range r.Form["access_methods"] {
		switch domain.AccessMethod(method) {
		case domain.AccessTerminal:
			form.TerminalAccess = true
		case domain.AccessDesktop:
			form.DesktopAccess = true
		case domain.AccessWeb:
			form.WebAccess = true
		}
	}
	for _, role := range r.Form["allowed_roles"] {
		switch domain.Role(role) {
		case domain.RoleUser:
			form.UserRole = true
		case domain.RoleAdministrator:
			form.AdministratorRole = true
		}
	}
	defaultCPU, err := parsePositiveInt(form.DefaultCPUMillis)
	if err != nil {
		return form, workspace.TemplateInput{}, workspace.ErrInvalidTemplate
	}
	maxCPU, err := parsePositiveInt(form.MaxCPUMillis)
	if err != nil {
		return form, workspace.TemplateInput{}, workspace.ErrInvalidTemplate
	}
	defaultMemory, err := parseResource(form.DefaultMemoryMiB, 1<<20)
	if err != nil {
		return form, workspace.TemplateInput{}, workspace.ErrInvalidTemplate
	}
	maxMemory, err := parseResource(form.MaxMemoryMiB, 1<<20)
	if err != nil {
		return form, workspace.TemplateInput{}, workspace.ErrInvalidTemplate
	}
	defaultStorage, err := parseResource(form.DefaultStorageGiB, 1<<30)
	if err != nil {
		return form, workspace.TemplateInput{}, workspace.ErrInvalidTemplate
	}
	methods := make([]domain.AccessMethod, 0, 3)
	if form.TerminalAccess {
		methods = append(methods, domain.AccessTerminal)
	}
	if form.DesktopAccess {
		methods = append(methods, domain.AccessDesktop)
	}
	if form.WebAccess {
		methods = append(methods, domain.AccessWeb)
	}
	roles := make([]domain.Role, 0, 2)
	if form.UserRole {
		roles = append(roles, domain.RoleUser)
	}
	if form.AdministratorRole {
		roles = append(roles, domain.RoleAdministrator)
	}
	return form, workspace.TemplateInput{
		Name:                form.Name,
		Description:         form.Description,
		ImageReference:      form.ImageReference,
		ImageDigest:         form.ImageDigest,
		DefaultCPUMillis:    defaultCPU,
		MaxCPUMillis:        maxCPU,
		DefaultMemoryBytes:  defaultMemory,
		MaxMemoryBytes:      maxMemory,
		DefaultStorageBytes: defaultStorage,
		AccessMethods:       methods,
		AllowedRoles:        roles,
		Enabled:             form.Enabled,
	}, nil
}

func parsePositiveInt(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("value must be positive")
	}
	return parsed, nil
}

func parseResource(value string, multiplier int64) (int64, error) {
	parsed, err := parsePositiveInt(value)
	if err != nil || parsed > (1<<62)/multiplier {
		return 0, errors.New("resource value is invalid")
	}
	return parsed * multiplier, nil
}

func defaultTemplateForm() templateFormData {
	return templateFormData{DefaultCPUMillis: "1000", MaxCPUMillis: "4000", DefaultMemoryMiB: "2048", MaxMemoryMiB: "8192", DefaultStorageGiB: "20", TerminalAccess: true, UserRole: true, Enabled: true}
}

func templateFormFromDomain(template domain.WorkspaceTemplate) templateFormData {
	form := templateFormData{ID: template.ID, Editing: true, Name: template.Name, Description: template.Description, ImageReference: template.ImageReference, ImageDigest: template.ImageDigest, DefaultCPUMillis: strconv.FormatInt(template.DefaultCPUMillis, 10), MaxCPUMillis: strconv.FormatInt(template.MaxCPUMillis, 10), DefaultMemoryMiB: strconv.FormatInt(template.DefaultMemoryBytes/(1<<20), 10), MaxMemoryMiB: strconv.FormatInt(template.MaxMemoryBytes/(1<<20), 10), DefaultStorageGiB: strconv.FormatInt(template.DefaultStorageBytes/(1<<30), 10), Enabled: template.Enabled}
	for _, method := range template.AccessMethods {
		switch method {
		case domain.AccessTerminal:
			form.TerminalAccess = true
		case domain.AccessDesktop:
			form.DesktopAccess = true
		case domain.AccessWeb:
			form.WebAccess = true
		}
	}
	for _, role := range template.AllowedRoles {
		switch role {
		case domain.RoleUser:
			form.UserRole = true
		case domain.RoleAdministrator:
			form.AdministratorRole = true
		}
	}
	return form
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

func templateFormError(err error) string {
	switch {
	case errors.Is(err, workspace.ErrInvalidTemplate):
		return "Check the image reference, resource limits, access methods, roles, and required fields."
	case errors.Is(err, repository.ErrConflict):
		return "A workspace template with that name already exists."
	default:
		return "The workspace template could not be saved."
	}
}

func templateActionError(err error) string {
	if errors.Is(err, repository.ErrNotFound) {
		return "The workspace template was not found."
	}
	return "The workspace template state could not be changed."
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
