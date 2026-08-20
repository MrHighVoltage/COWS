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
	"hash/fnv"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/files"
	"github.com/cows-project/cows/internal/notifications"
	"github.com/cows-project/cows/internal/quota"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
	"github.com/cows-project/cows/internal/workspace"
	webassets "github.com/cows-project/cows/web"
)

const (
	sessionCookieName            = "cows_session"
	csrfCookieName               = "cows_csrf"
	terminalIdleTimeout          = 15 * time.Minute
	terminalMaxLifetime          = time.Hour
	terminalSessionCheckInterval = 30 * time.Second
)

type Options struct {
	CookieSecure         bool
	SessionLifetime      time.Duration
	Logger               *slog.Logger
	LoginLimiter         *auth.LoginLimiter
	RegistrationEnabled  bool
	RegistrationLimiter  *auth.LoginLimiter
	Notifications        *notifications.Service
	ExternalBaseURL      string
	PasswordResetEnabled bool
	Store                repository.Store
}

type Server struct {
	db          *sql.DB
	logger      *slog.Logger
	auth        *auth.Service
	workspace   *workspace.Service
	quota       *quota.Service
	runtime     runtime.Runtime
	files       *files.Service
	options     Options
	templates   *template.Template
	static      fs.FS
	userImports *userImportStore
	imagePullMu sync.Mutex
	imagePulls  map[string]*imagePullOperation
}

type healthSnapshot struct {
	Status    string
	Database  string
	CheckedAt string
}

// readinessCheckTimeout bounds the runtime connectivity probe so a stalled
// Podman socket cannot make GET /readyz hang past a supervisor's own probe
// timeout; the adapter's own client timeout is longer (10s).
const readinessCheckTimeout = 5 * time.Second

type readinessSnapshot struct {
	Status    string
	Database  string
	Runtime   string
	CheckedAt string
}

type userFormData struct {
	Username    string
	Email       string
	DisplayName string
	Role        string
	Error       string
}

type passwordFormData struct {
	Error string
}

type passwordResetFormData struct {
	Identifier string
	Token      string
	Error      string
	Notice     string
}

type registrationFormData struct {
	Username    string
	Email       string
	DisplayName string
	Error       string
}

type workspaceFormData struct {
	Name       string
	TemplateID string
	CPUMillis  string
	MemoryMiB  string
	Error      string
}

type quotaFormData struct {
	UserID               string
	GroupID              string
	MaxCPUMillis         string
	MaxMemoryMiB         string
	MaxStorageGiB        string
	MaxWorkspaces        string
	MaxRunningWorkspaces string
	Error                string
}

type settingsFormData struct {
	HostStorageGiB          string
	CPUOverbookingFactor    string
	MemoryOverbookingFactor string
	ReservedStorageGiB      string
	Error                   string
}

type allocationView struct {
	Summary       domain.AllocationSummary
	Quota         domain.UserQuota
	QuotaAssigned bool
	StorageKnown  bool
}

// resourceStripData is the "." context for the shared
// "workspaces-resource-strip" template. OOB marks the render as an
// out-of-band update piggybacked on a row-action response, so a start or
// stop moves the strip at the same moment it moves the row instead of
// waiting for the list's next poll.
type resourceStripData struct {
	Allocation *allocationView
	OOB        bool
}

type templateFormData struct {
	ID                     string
	Editing                bool
	Name                   string
	Description            string
	ImageReference         string
	ImageDigest            string
	DefaultCPUMillis       string
	MaxCPUMillis           string
	DefaultMemoryMiB       string
	MaxMemoryMiB           string
	ResourcesConfigurable  bool
	InitialConnectionHours string
	StoppedRetentionHours  string
	ConfigurationJSON      string
	TerminalRootAllowed    bool
	TerminalAccess         bool
	DesktopAccess          bool
	FilesAccess            bool
	LogsAccess             bool
	UserRole               bool
	AdministratorRole      bool
	GroupAccessMode        string
	AllowedGroupIDs        []string
	Enabled                bool
	Error                  string
}

type templateImageView struct {
	domain.WorkspaceTemplate
	CSRFToken string
	Status    string
	Message   string
	Pulling   bool
	// RowError is a transient, request-scoped message shown only in the
	// just-swapped-in row after a rejected dynamic row action.
	RowError string
}

type imagePullOperation struct {
	Reference string
	Status    string
	Message   string
}

type pageData struct {
	Title                       string
	User                        *domain.User
	Health                      healthSnapshot
	CSRFToken                   string
	Error                       string
	Notice                      string
	Users                       []domain.User
	Form                        userFormData
	Templates                   []domain.WorkspaceTemplate
	TemplateImages              []templateImageView
	TemplateImage               *templateImageView
	Template                    templateFormData
	Password                    passwordFormData
	PasswordReset               passwordResetFormData
	PasswordResetEnabled        bool
	Registration                registrationFormData
	RegistrationEnabled         bool
	Inspection                  *runtime.Inspection
	RuntimeError                string
	Workspaces                  []domain.Workspace
	Workspace                   workspaceFormData
	WorkspaceAvailabilityKnown  bool
	WorkspaceAvailableCPUMillis int64
	WorkspaceAvailableMemoryMiB int64
	AccessTab                   string
	Access                      workspaceAccess
	AccessFilesURL              string
	Quota                       quotaFormData
	QuotaAssigned               bool
	Settings                    *domain.HostSettings
	SettingsForm                settingsFormData
	Allocation                  *allocationView
	ActiveWorkspace             *domain.Workspace
	WorkspaceOwnerName          string
	WorkspaceAccess             map[string]workspaceAccess
	FileListing                 *files.Listing
	FileMounts                  []workspace.FileMount
	FileError                   string
	AccessLogsURL               string
	LogLines                    []runtime.LogLine
	LogError                    string
	Groups                      []domain.Group
	UserGroupIDs                map[string][]string
	UserGroups                  map[string][]domain.Group
	EditUser                    *domain.User
	EditUserGroups              []string
	EditGroup                   *domain.Group
	GroupQuota                  quotaFormData
	GroupQuotaAssigned          bool
	RuntimeWorkspaces           []runtimeWorkspaceView
	UserImport                  *userImportPageData
	AuditEvents                 []domain.AuditRecord
	AuditEventFilter            string
	Metrics                     *metricsView
	RetainedVolumes             []retainedVolumeView
	MyRetainedVolumes           []retainedVolumeView
	MyRetainedDirectories       []domain.RetainedWorkspaceDirectory
	RetainedDirectories         []retainedDirectoryView
	ReattachVolumeOptions       map[string][]reattachChoice
	ReattachDirectoryOptions    []reattachDirectoryChoice
}

type retainedVolumeView struct {
	domain.RetainedWorkspaceVolume
	RuntimePresent bool
	CSRFToken      string
	// RowError is a transient, request-scoped message shown only in the
	// just-swapped-in row after a rejected dynamic row action.
	RowError string
}

type retainedDirectoryView struct {
	domain.RetainedWorkspaceDirectory
	CSRFToken string
	// RowError is a transient, request-scoped message shown only in the
	// just-swapped-in row after a rejected dynamic row action.
	RowError string
}

// reattachChoice is one selectable retained-volume tombstone offered for a
// specific mount name on the workspace-creation form (self-service storage
// reattachment, decision 0025).
type reattachChoice struct {
	WorkspaceID   string
	WorkspaceName string
	RetainedAt    time.Time
}

// reattachDirectoryChoice is the directory equivalent. Unlike volumes, a
// directory tombstone covers every directory mount it was archived with at
// once, so it is offered once per tombstone rather than once per mount;
// MountNames lists what it will restore for display.
type reattachDirectoryChoice struct {
	WorkspaceID   string
	WorkspaceName string
	RetainedAt    time.Time
	MountNames    string
}

type metricsWorkspaceView struct {
	Workspace domain.Workspace
	Usage     runtime.ResourceUsage
	Known     bool
}

type metricsView struct {
	Capacity        runtime.HostCapacity
	Allocated       domain.AllocationSummary
	Workspaces      []metricsWorkspaceView
	ObservedAt      time.Time
	CapacityError   string
	AllocationError string
}

type workspaceAccess struct {
	Terminal bool
	Desktop  bool
	Files    bool
	Logs     bool
}

// userRowData is the "." context for the shared "admin-user-row" template
// fragment, mirroring workspaceRowData's role for the workspace list.
type userRowData struct {
	Target        domain.User
	Groups        []domain.Group
	CSRFToken     string
	CurrentUserID string
	RowError      string
}

// workspaceRowData is the "." context for the shared "workspace-row"
// template fragment, used both inside the full workspace list and as the
// standalone HTMX response an action button swaps in, so a single template
// definition covers both without duplicating the row markup.
type workspaceRowData struct {
	Workspace domain.Workspace
	Access    workspaceAccess
	CSRFToken string
	// RowError is a transient, request-scoped message (e.g. "workspace is
	// still running") shown only in the just-swapped-in row; it is never
	// persisted and disappears on the next full list refresh.
	RowError string
}

// workspaceDetailHeaderData is the Detail page's equivalent of
// workspaceRowData: the badge/name/status/actions block that a lifecycle
// action re-renders in place via htmx, instead of the whole page.
type workspaceDetailHeaderData struct {
	Workspace domain.Workspace
	Access    workspaceAccess
	CSRFToken string
	OwnerName string
	RowError  string
}

type runtimeWorkspaceView struct {
	Observed       runtime.ObservedWorkspace
	Owner          string
	OwnerID        string
	Template       string
	WorkspaceID    string
	Access         workspaceAccess
	Managed        bool
	RuntimePresent bool
	CSRFToken      string
	// RowError is a transient, request-scoped message shown only in the
	// just-swapped-in row after a rejected dynamic row action.
	RowError string
}

func New(db *sql.DB, authService *auth.Service, templateService *workspace.Service, quotaService *quota.Service, runtimeAdapter runtime.Runtime, options Options) (*Server, error) {
	if options.SessionLifetime <= 0 {
		options.SessionLifetime = 8 * time.Hour
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.LoginLimiter == nil {
		options.LoginLimiter, _ = auth.NewLoginLimiter(auth.DefaultLoginFailureLimit, auth.DefaultLoginFailureWindow)
	}
	if options.RegistrationLimiter == nil {
		options.RegistrationLimiter, _ = auth.NewLoginLimiter(auth.DefaultLoginFailureLimit, auth.DefaultLoginFailureWindow)
	}
	templates, err := template.New("cows").Funcs(template.FuncMap{
		"mib":           func(bytes int64) int64 { return bytes / (1 << 20) },
		"gib":           func(bytes int64) int64 { return bytes / (1 << 30) },
		"quotaClass":    quotaProgressClass,
		"quotaOver":     quotaOver,
		"quotaExceeded": quotaExceeded,
		"timeoutPhase": func(value domain.Workspace) workspace.TimeoutStatus {
			return workspace.EvaluateTimeouts(value, time.Now())
		},
		"resourceStrip": func(data pageData) resourceStripData {
			return resourceStripData{Allocation: data.Allocation}
		},
		"workspaceRow": func(data pageData, value domain.Workspace) workspaceRowData {
			return workspaceRowData{Workspace: value, Access: data.WorkspaceAccess[value.ID], CSRFToken: data.CSRFToken}
		},
		"workspaceDetailHeader": func(data pageData) workspaceDetailHeaderData {
			value := domain.Workspace{}
			if data.ActiveWorkspace != nil {
				value = *data.ActiveWorkspace
			}
			return workspaceDetailHeaderData{Workspace: value, Access: data.Access, CSRFToken: data.CSRFToken, OwnerName: data.WorkspaceOwnerName, RowError: data.Error}
		},
		"userRow": func(data pageData, value domain.User) userRowData {
			currentUserID := ""
			if data.User != nil {
				currentUserID = data.User.ID
			}
			return userRowData{Target: value, Groups: data.UserGroups[value.ID], CSRFToken: data.CSRFToken, CurrentUserID: currentUserID}
		},
		"timeoutPhaseText": func(value workspace.TimeoutPhase) string { return timeoutPhaseText(value) },
		"timeoutText":      func(seconds int64) string { return formatTimeout(seconds) },
		"timeoutCountdownText": func(status workspace.TimeoutStatus) string {
			return timeoutCountdownText(status, time.Now())
		},
		"timeoutUrgencyClass": func(status workspace.TimeoutStatus) string {
			return timeoutUrgencyClass(status, time.Now())
		},
		"workspaceInitials":     workspaceInitials,
		"templateHue":           templateHue,
		"relativeTime":          relativeTime,
		"workspaceStatusBucket": workspaceStatusBucket,
		"workspaceStatusCounts": workspaceStatusCounts,
		"workspaceStatusDot":    workspaceStatusDot,
		"stateStatusDot": func(state any) statusDotView {
			if value, ok := state.(runtime.State); ok {
				return stateStatusDot(string(value))
			}
			return stateStatusDot(fmt.Sprint(state))
		},
		"booleanStatusDot": booleanStatusDot,
		// statusDot is a generic escape hatch for one-off status readouts
		// that don't fit the dedicated helpers above (workspace lifecycle,
		// a runtime state string, or a plain on/off pair): bucket is one of
		// "ok", "warn", "bad", or "neutral".
		"statusDot": func(label, bucket string) statusDotView {
			return statusDotView{Label: label, Bucket: bucket}
		},
		"pulsingStatusDot": func(label, bucket string) statusDotView {
			return statusDotView{Label: label, Bucket: bucket, Pulsing: true}
		},
		"volumeMounts": func(mounts []domain.TemplateMount) []domain.TemplateMount {
			volumes := make([]domain.TemplateMount, 0, len(mounts))
			for _, mount := range mounts {
				if mount.Type == domain.TemplateMountVolume {
					volumes = append(volumes, mount)
				}
			}
			return volumes
		},
		"operationText": func(value string) string { return operationText(value) },
		"accessURL": func(workspaceID, tab string) string {
			return "/workspaces/" + workspaceID + "/access?tab=" + url.QueryEscape(tab)
		},
		"observedErrorText": func(value string) string {
			return observedErrorText(value)
		},
		"fileSize": formatFileSize,
		"cpuPercent": func(value int64) string {
			return fmt.Sprintf("%.1f%%", float64(value)/1000)
		},
		"hasID": func(values []string, wanted string) bool {
			for _, value := range values {
				if value == wanted {
					return true
				}
			}
			return false
		},
		"fileURL": func(workspaceID, mountName, relativePath string) string {
			values := url.Values{}
			values.Set("mount", mountName)
			values.Set("path", relativePath)
			values.Set("tab", "files")
			return "/workspaces/" + workspaceID + "/access?" + values.Encode()
		},
		"fileParent": func(relativePath string) string {
			parent := path.Dir(relativePath)
			if parent == "" {
				return "."
			}
			return parent
		},
		"deadlineText": func(value time.Time) string {
			if value.IsZero() {
				return ""
			}
			return value.Local().Format("2006-01-02 15:04")
		},
		"observedTime": func(value time.Time) string {
			if value.IsZero() {
				return "Not available"
			}
			return value.Local().Format("2006-01-02 15:04")
		},
		"metadataJSON": func(value map[string]string) string {
			encoded, _ := json.Marshal(value)
			return string(encoded)
		},
	}).ParseFS(webassets.Files, "templates/layouts/*.html", "templates/pages/*.html", "templates/fragments/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	static, err := fs.Sub(webassets.Files, "static")
	if err != nil {
		return nil, fmt.Errorf("open static assets: %w", err)
	}
	fileAccessRuntime, _ := runtimeAdapter.(runtime.FileAccessRuntime)
	return &Server{db: db, logger: options.Logger, auth: authService, workspace: templateService, quota: quotaService, runtime: runtimeAdapter, files: files.New(templateService, fileAccessRuntime), options: options, templates: templates, static: static, userImports: newUserImportStore(), imagePulls: make(map[string]*imagePullOperation)}, nil
}

func quotaProgressClass(current, limit int64) string {
	if limit <= 0 {
		return ""
	}
	if current >= limit {
		return "quota-progress-danger"
	}
	if current < 0 {
		current = 0
	}
	// Use the remaining fifth instead of multiplying values, which keeps the
	// comparison safe for large byte-based quotas.
	if current >= limit-limit/5 {
		return "quota-progress-warning"
	}
	return ""
}

func quotaOver(current, limit int64) bool {
	return limit > 0 && current > limit
}

func quotaExceeded(summary domain.AllocationSummary, limit domain.UserQuota, storageKnown bool) bool {
	return quotaOver(summary.Resources.CPUMillis, limit.MaxCPUMillis) ||
		quotaOver(summary.Resources.MemoryBytes, limit.MaxMemoryBytes) ||
		(storageKnown && quotaOver(summary.Resources.StorageBytes, limit.MaxStorageBytes)) ||
		quotaOver(summary.WorkspaceCount, limit.MaxWorkspaces) ||
		quotaOver(summary.RunningWorkspaceCount, limit.MaxRunningWorkspaces)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /fragments/health", s.healthFragment)
	mux.HandleFunc("GET /login", s.loginGet)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("GET /password/reset", s.passwordResetGet)
	mux.HandleFunc("POST /password/reset/request", s.passwordResetRequestPost)
	mux.HandleFunc("GET /password/reset/confirm", s.passwordResetConfirmGet)
	mux.HandleFunc("POST /password/reset/confirm", s.passwordResetConfirmPost)
	mux.HandleFunc("GET /register", s.registerGet)
	mux.HandleFunc("POST /register", s.registerPost)
	mux.HandleFunc("POST /logout", s.logoutPost)
	mux.HandleFunc("GET /account/password", s.passwordGet)
	mux.HandleFunc("POST /account/password", s.passwordPost)
	mux.HandleFunc("GET /workspaces", s.workspaces)
	mux.HandleFunc("GET /fragments/workspaces", s.workspacesFragment)
	mux.HandleFunc("GET /workspaces/new", s.workspacesNew)
	mux.HandleFunc("POST /workspaces", s.workspacesCreate)
	mux.HandleFunc("POST /workspaces/{id}/start", s.workspaceStart)
	mux.HandleFunc("POST /workspaces/{id}/stop", s.workspaceStop)
	mux.HandleFunc("POST /workspaces/{id}/restart", s.workspaceRestart)
	mux.HandleFunc("POST /workspaces/{id}/delete", s.workspaceDelete)
	mux.HandleFunc("GET /workspaces/{id}/terminal", s.workspaceTerminalPage)
	mux.HandleFunc("GET /workspaces/{id}/terminal/ws", s.workspaceTerminalWebSocket)
	mux.HandleFunc("GET /workspaces/{id}/access", s.workspaceAccessPage)
	mux.HandleFunc("GET /workspaces/{id}/access/files", s.workspaceAccessFilesFragment)
	mux.HandleFunc("GET /workspaces/{id}/access/logs", s.workspaceAccessLogsFragment)
	mux.HandleFunc("GET /workspaces/{id}/access/header", s.workspaceAccessHeaderFragment)
	mux.HandleFunc("GET /workspaces/{id}/access/sidebar", s.workspaceAccessSidebarFragment)
	mux.HandleFunc("GET /workspaces/{id}/desktop", s.workspaceDesktopPage)
	mux.HandleFunc("GET /workspaces/{id}/desktop/credentials", s.workspaceDesktopCredentials)
	mux.HandleFunc("GET /workspaces/{id}/desktop/ws", s.workspaceDesktopWebSocket)
	mux.HandleFunc("GET /workspaces/{id}/files", s.workspaceFilesPage)
	mux.HandleFunc("GET /workspaces/{id}/files/download", s.workspaceFileDownload)
	mux.HandleFunc("GET /workspaces/{id}/files/download.zip", s.workspaceFileDownloadZip)
	mux.HandleFunc("POST /workspaces/{id}/files/mkdir", s.workspaceFileMkdir)
	mux.HandleFunc("POST /workspaces/{id}/files/delete", s.workspaceFileDelete)
	mux.HandleFunc("POST /workspaces/{id}/files/rename", s.workspaceFileRename)
	mux.HandleFunc("POST /workspaces/{id}/files/upload", s.workspaceFileUpload)
	mux.HandleFunc("GET /storage", s.storagePage)
	mux.HandleFunc("GET /storage/volumes/{workspace_id}/{mount_name}/download.zip", s.storageVolumeDownload)
	mux.HandleFunc("POST /storage/volumes/{workspace_id}/{mount_name}/delete", s.storageVolumeDelete)
	mux.HandleFunc("GET /storage/directories/{workspace_id}/download.zip", s.storageDirectoryDownload)
	mux.HandleFunc("POST /storage/directories/{workspace_id}/delete", s.storageDirectoryDelete)
	mux.HandleFunc("GET /admin/users", s.adminUsers)
	mux.HandleFunc("GET /admin/users/import", s.adminUsersImport)
	mux.HandleFunc("POST /admin/users/import/preview", s.adminUsersImportPreview)
	mux.HandleFunc("POST /admin/users/import/commit", s.adminUsersImportCommit)
	mux.HandleFunc("GET /admin/users/import/download/{token}", s.adminUsersImportDownload)
	mux.HandleFunc("GET /admin/users/new", s.adminUsersNew)
	mux.HandleFunc("GET /admin/users/{id}/edit", s.adminUserEdit)
	mux.HandleFunc("POST /admin/users", s.adminUsersCreate)
	mux.HandleFunc("POST /admin/users/{id}/disabled", s.adminUserDisabled)
	mux.HandleFunc("POST /admin/users/{id}/delete", s.adminUserDelete)
	mux.HandleFunc("POST /admin/users/{id}/groups", s.adminUserGroups)
	mux.HandleFunc("GET /admin/groups", s.adminGroups)
	mux.HandleFunc("POST /admin/groups", s.adminGroupsCreate)
	mux.HandleFunc("POST /admin/users/{id}/quota", s.adminQuotaUpdate)
	mux.HandleFunc("POST /admin/users/{id}/quota/delete", s.adminQuotaDelete)
	mux.HandleFunc("GET /admin/groups/{id}/edit", s.adminGroupEdit)
	mux.HandleFunc("POST /admin/groups/{id}/delete", s.adminGroupDelete)
	mux.HandleFunc("POST /admin/groups/{id}/quota", s.adminGroupQuotaUpdate)
	mux.HandleFunc("POST /admin/groups/{id}/quota/delete", s.adminGroupQuotaDelete)
	mux.HandleFunc("GET /admin/settings", s.adminSettings)
	mux.HandleFunc("POST /admin/settings", s.adminSettingsUpdate)
	mux.HandleFunc("GET /admin/templates", s.adminTemplates)
	mux.HandleFunc("GET /admin/templates/new", s.adminTemplatesNew)
	mux.HandleFunc("POST /admin/templates", s.adminTemplatesCreate)
	mux.HandleFunc("GET /admin/templates/{id}/edit", s.adminTemplatesEdit)
	mux.HandleFunc("GET /admin/templates/{id}/copy", s.adminTemplatesCopy)
	mux.HandleFunc("POST /admin/templates/{id}", s.adminTemplatesUpdate)
	mux.HandleFunc("POST /admin/templates/{id}/enabled", s.adminTemplateEnabled)
	mux.HandleFunc("POST /admin/templates/{id}/image/pull", s.adminTemplateImagePull)
	mux.HandleFunc("GET /admin/templates/{id}/image/status", s.adminTemplateImageStatus)
	mux.HandleFunc("GET /admin/runtime", s.adminRuntime)
	mux.HandleFunc("GET /admin/audit", s.adminAudit)
	mux.HandleFunc("GET /admin/metrics", s.adminMetrics)
	mux.HandleFunc("GET /admin/volumes", s.adminVolumes)
	mux.HandleFunc("GET /admin/volumes/{workspace_id}/{mount_name}/download.zip", s.adminVolumeDownload)
	mux.HandleFunc("POST /admin/volumes/{workspace_id}/{mount_name}/delete", s.adminVolumeDelete)
	mux.HandleFunc("GET /admin/directories", s.adminDirectories)
	mux.HandleFunc("GET /admin/directories/{workspace_id}/download.zip", s.adminDirectoryDownload)
	mux.HandleFunc("POST /admin/directories/{workspace_id}/delete", s.adminDirectoryDelete)
	mux.HandleFunc("GET /", s.home)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
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
	if user != nil && user.MustChangePassword {
		http.Redirect(w, r, "/account/password", http.StatusSeeOther)
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
	s.render(w, http.StatusOK, "login-page", pageData{Title: "Sign in | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: s.options.RegistrationEnabled, PasswordResetEnabled: s.options.PasswordResetEnabled})
}

func (s *Server) passwordResetGet(w http.ResponseWriter, r *http.Request) {
	if !s.options.PasswordResetEnabled {
		http.NotFound(w, r)
		return
	}
	data := pageData{Title: "Reset password | COWS", CSRFToken: s.ensureCSRF(w, r), PasswordResetEnabled: true}
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		data.PasswordReset.Token = token
		s.render(w, http.StatusOK, "password-reset-confirm-page", data)
		return
	}
	s.render(w, http.StatusOK, "password-reset-request-page", data)
}

func (s *Server) passwordResetRequestPost(w http.ResponseWriter, r *http.Request) {
	if !s.options.PasswordResetEnabled || s.options.Notifications == nil {
		http.NotFound(w, r)
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
	data := pageData{Title: "Reset password | COWS", CSRFToken: s.ensureCSRF(w, r), PasswordResetEnabled: true}
	key := "password-reset:" + loginRateLimitKey(r)
	if !s.options.LoginLimiter.Allow(key) {
		data.PasswordReset.Notice = "If the account is eligible, a reset message will be sent. Check your email or try again later."
		s.render(w, http.StatusOK, "password-reset-request-page", data)
		return
	}
	request, err := s.auth.RequestPasswordReset(r.Context(), r.FormValue("identifier"))
	if err == nil && request.Token != "" {
		err = s.options.Notifications.EnqueuePasswordReset(r.Context(), request.User.ID, request.User.Email, request.Token, s.options.ExternalBaseURL, request.ExpiresAt)
	}
	s.options.LoginLimiter.Failure(key)
	// Do not distinguish unknown accounts or delivery failures in this response.
	data.PasswordReset.Notice = "If the account is eligible, a password reset message has been queued. Check your email."
	if err != nil {
		data.PasswordReset.Notice = "If the account is eligible, a password reset message will be sent. Check your email or try again later."
	}
	s.render(w, http.StatusOK, "password-reset-request-page", data)
}

func (s *Server) passwordResetConfirmGet(w http.ResponseWriter, r *http.Request) {
	if !s.options.PasswordResetEnabled {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" || len(token) > 256 {
		http.Redirect(w, r, "/password/reset", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "password-reset-confirm-page", pageData{Title: "Choose new password | COWS", CSRFToken: s.ensureCSRF(w, r), PasswordResetEnabled: true, PasswordReset: passwordResetFormData{Token: token}})
}

func (s *Server) passwordResetConfirmPost(w http.ResponseWriter, r *http.Request) {
	if !s.options.PasswordResetEnabled {
		http.NotFound(w, r)
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
	data := pageData{Title: "Choose new password | COWS", CSRFToken: s.ensureCSRF(w, r), PasswordResetEnabled: true, PasswordReset: passwordResetFormData{Token: r.FormValue("token")}}
	if r.FormValue("new_password") != r.FormValue("confirm_password") {
		data.PasswordReset.Error = "The new passwords do not match."
		s.render(w, http.StatusBadRequest, "password-reset-confirm-page", data)
		return
	}
	if err := s.auth.ResetPassword(r.Context(), r.FormValue("token"), r.FormValue("new_password")); err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidInput):
			data.PasswordReset.Error = "The new password must be between 12 and 72 characters."
		default:
			data.PasswordReset.Error = "This reset link is invalid or expired. Request a new one."
		}
		s.render(w, http.StatusBadRequest, "password-reset-confirm-page", data)
		return
	}
	s.render(w, http.StatusOK, "login-page", pageData{Title: "Sign in | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: s.options.RegistrationEnabled, PasswordResetEnabled: s.options.PasswordResetEnabled, Notice: "Your password was changed. You can now sign in."})
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
			Title: "Sign in | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: s.options.RegistrationEnabled,
			Error: "Too many sign-in attempts. Try again later.",
		})
		return
	}
	user, token, err := s.auth.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		s.options.LoginLimiter.Failure(key)
		s.render(w, http.StatusUnauthorized, "login-page", pageData{
			Title: "Sign in | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: s.options.RegistrationEnabled,
			Error: "Invalid username or password.",
		})
		return
	}
	s.options.LoginLimiter.Success(key)
	s.setSessionCookie(w, token)
	if user.MustChangePassword {
		http.Redirect(w, r, "/account/password", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) registerGet(w http.ResponseWriter, r *http.Request) {
	if !s.options.RegistrationEnabled {
		http.NotFound(w, r)
		return
	}
	user, err := s.currentUser(r)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return
	}
	if user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "register-page", pageData{Title: "Register | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: true})
}

func (s *Server) registerPost(w http.ResponseWriter, r *http.Request) {
	if !s.options.RegistrationEnabled {
		http.NotFound(w, r)
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
	key := "registration:" + loginRateLimitKey(r)
	if !s.options.RegistrationLimiter.Allow(key) {
		retryAfter := int(s.options.RegistrationLimiter.RetryAfter(key).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		s.render(w, http.StatusTooManyRequests, "register-page", pageData{Title: "Register | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: true, Registration: registrationFormData{Error: "Too many registration attempts. Try again later."}})
		return
	}
	form := registrationFormData{Username: r.FormValue("username"), Email: r.FormValue("email"), DisplayName: r.FormValue("display_name")}
	_, err := s.auth.Register(r.Context(), auth.RegisterUserInput{Username: form.Username, Email: form.Email, DisplayName: form.DisplayName, Password: r.FormValue("password"), PasswordConfirmation: r.FormValue("confirm_password")})
	if err != nil {
		s.options.RegistrationLimiter.Failure(key)
		form.Error = registrationFormError(err)
		s.render(w, http.StatusBadRequest, "register-page", pageData{Title: "Register | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: true, Registration: form})
		return
	}
	s.options.RegistrationLimiter.Success(key)
	s.render(w, http.StatusOK, "login-page", pageData{Title: "Sign in | COWS", CSRFToken: s.ensureCSRF(w, r), RegistrationEnabled: true, Notice: "Your account was created. You can now sign in."})
}

func (s *Server) passwordGet(w http.ResponseWriter, r *http.Request) {
	user, err := s.currentUser(r)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return
	}
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "password-page", pageData{Title: "Change password | COWS", User: user, CSRFToken: s.ensureCSRF(w, r)})
}

func (s *Server) passwordPost(w http.ResponseWriter, r *http.Request) {
	user, err := s.currentUser(r)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return
	}
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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
	if r.FormValue("new_password") != r.FormValue("confirm_password") {
		s.render(w, http.StatusBadRequest, "password-page", pageData{Title: "Change password | COWS", User: user, CSRFToken: s.ensureCSRF(w, r), Password: passwordFormData{Error: "The new passwords do not match."}})
		return
	}
	if err := s.auth.ChangePassword(r.Context(), user.ID, r.FormValue("current_password"), r.FormValue("new_password")); err != nil {
		message := "The password could not be changed."
		if errors.Is(err, auth.ErrInvalidCredentials) {
			message = "The current password is incorrect."
		} else if errors.Is(err, auth.ErrInvalidInput) {
			message = "The new password must be between 12 and 72 characters."
		}
		s.render(w, http.StatusBadRequest, "password-page", pageData{Title: "Change password | COWS", User: user, CSRFToken: s.ensureCSRF(w, r), Password: passwordFormData{Error: message}})
		return
	}
	if cookie, cerr := r.Cookie(sessionCookieName); cerr == nil {
		_ = s.auth.RevokeOtherSessions(r.Context(), user.ID, cookie.Value)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	data, err := s.workspacePageData(r.Context(), user)
	if err != nil {
		http.Error(w, "failed to load workspaces", http.StatusInternalServerError)
		return
	}
	data.Title = "Workspaces | COWS"
	data.User = user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "workspaces-page", data)
}

func (s *Server) workspacesFragment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	data, err := s.workspacePageData(r.Context(), user)
	if err != nil {
		http.Error(w, "failed to load workspaces", http.StatusInternalServerError)
		return
	}
	data.User = user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "workspaces-list-fragment", data)
}

func (s *Server) workspacePageData(ctx context.Context, user *domain.User) (pageData, error) {
	workspaces, err := s.workspace.ListWorkspaces(ctx, user.ID)
	if err != nil {
		return pageData{}, err
	}
	allocation, err := s.allocationViewForWorkspaces(ctx, user.ID, workspaces)
	if err != nil {
		return pageData{}, err
	}
	data := pageData{Workspaces: workspaces, Allocation: allocation, WorkspaceAccess: make(map[string]workspaceAccess, len(workspaces))}
	methodsByWorkspace, err := s.workspace.WorkspaceAccessMethodsForWorkspaces(ctx, user.ID, workspaces)
	if err != nil {
		return pageData{}, err
	}
	for _, value := range workspaces {
		methods := methodsByWorkspace[value.ID]
		access := workspaceAccess{}
		for _, method := range methods {
			switch method {
			case domain.AccessTerminal:
				access.Terminal = true
			case domain.AccessDesktop:
				access.Desktop = true
			case domain.AccessFiles:
				access.Files = true
			case domain.AccessLogs:
				access.Logs = true
			}
		}
		data.WorkspaceAccess[value.ID] = access
	}
	return data, nil
}

// allocationViewForWorkspaces builds the resource strip's view over an
// already-loaded workspace list, so the page, the list fragment and
// row actions all report the same numbers.
func (s *Server) allocationViewForWorkspaces(ctx context.Context, userID string, workspaces []domain.Workspace) (*allocationView, error) {
	summary, storageKnown, err := s.workspace.AllocationSummaryForWorkspaces(ctx, userID, workspaces)
	if err != nil {
		return nil, err
	}
	view := &allocationView{Summary: summary, StorageKnown: storageKnown}
	if s.quota != nil {
		assigned, quotaErr := s.quota.GetForUser(ctx, userID)
		if quotaErr == nil {
			view.Quota = assigned
			view.QuotaAssigned = true
		} else if !errors.Is(quotaErr, repository.ErrNotFound) {
			return nil, quotaErr
		}
	}
	return view, nil
}

// writeResourceStripOOB appends an out-of-band resource strip to a
// row-action response so the totals move with the row. It is best-effort:
// a failure here leaves the strip untouched until the list's next poll
// rather than failing the action the user actually asked for.
func (s *Server) writeResourceStripOOB(ctx context.Context, w http.ResponseWriter, userID string) {
	workspaces, err := s.workspace.ListWorkspaces(ctx, userID)
	if err != nil {
		return
	}
	allocation, err := s.allocationViewForWorkspaces(ctx, userID, workspaces)
	if err != nil {
		return
	}
	_ = s.templates.ExecuteTemplate(w, "workspaces-resource-strip", resourceStripData{Allocation: allocation, OOB: true})
}

func (s *Server) workspacesNew(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	templates, err := s.workspace.ListAvailableTemplates(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "failed to load workspace templates", http.StatusInternalServerError)
		return
	}
	volumeOptions, directoryOptions := s.reattachOptionsForUser(r.Context(), user.ID)
	data := pageData{Title: "Create workspace | COWS", User: user, Templates: templates, CSRFToken: s.ensureCSRF(w, r),
		ReattachVolumeOptions: volumeOptions, ReattachDirectoryOptions: directoryOptions}
	s.setWorkspaceAvailability(r.Context(), user.ID, &data)
	s.render(w, http.StatusOK, "workspace-new-page", data)
}

// reattachOptionsForUser loads the current user's own retained storage for
// the workspace-creation form's reattachment selectors (decision 0025).
// Volume options are keyed by mount name, matching how the backend matches
// them at submission time; directory options are offered once per tombstone
// since one tombstone restores every directory mount it was archived with.
func (s *Server) reattachOptionsForUser(ctx context.Context, userID string) (map[string][]reattachChoice, []reattachDirectoryChoice) {
	if s.options.Store == nil {
		return nil, nil
	}
	volumes, err := s.options.Store.ListRetainedWorkspaceVolumesForOwner(ctx, userID)
	if err != nil {
		return nil, nil
	}
	volumeOptions := make(map[string][]reattachChoice, len(volumes))
	for _, volume := range volumes {
		name := volume.WorkspaceName
		if name == "" {
			name = volume.WorkspaceID
		}
		volumeOptions[volume.MountName] = append(volumeOptions[volume.MountName], reattachChoice{WorkspaceID: volume.WorkspaceID, WorkspaceName: name, RetainedAt: volume.RetainedAt})
	}
	directories, err := s.options.Store.ListRetainedWorkspaceDirectoriesForOwner(ctx, userID)
	if err != nil {
		return volumeOptions, nil
	}
	directoryOptions := make([]reattachDirectoryChoice, 0, len(directories))
	for _, directory := range directories {
		name := directory.WorkspaceName
		if name == "" {
			name = directory.WorkspaceID
		}
		mountNames := make([]string, 0, len(directory.Mounts))
		for _, mount := range directory.Mounts {
			mountNames = append(mountNames, mount.Name)
		}
		directoryOptions = append(directoryOptions, reattachDirectoryChoice{WorkspaceID: directory.WorkspaceID, WorkspaceName: name, RetainedAt: directory.RetainedAt, MountNames: strings.Join(mountNames, ", ")})
	}
	return volumeOptions, directoryOptions
}

func (s *Server) workspacesCreate(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
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
	form := workspaceFormData{Name: r.FormValue("name"), TemplateID: r.FormValue("template_id"), CPUMillis: r.FormValue("cpu_millis"), MemoryMiB: r.FormValue("memory_mib")}
	reattachVolumesFrom := reattachVolumeSelections(r.PostForm)
	reattachDirectoriesFrom := strings.TrimSpace(r.FormValue("reattach_directories"))
	cpuMillis, err := parseOptionalPositiveInt(form.CPUMillis)
	if err != nil {
		form.Error = "The selected CPU value is invalid."
	} else {
		memoryMiB, memoryErr := parseOptionalPositiveInt(form.MemoryMiB)
		if memoryErr != nil || memoryMiB > (1<<62)/(1<<20) {
			form.Error = "The selected memory value is invalid."
		} else if _, err := s.workspace.CreateWorkspace(r.Context(), user.ID, workspace.CreateWorkspaceInput{
			Name: form.Name, TemplateID: form.TemplateID, CPUMillis: cpuMillis, MemoryBytes: memoryMiB * (1 << 20),
			ReattachVolumesFrom: reattachVolumesFrom, ReattachDirectoriesFrom: reattachDirectoriesFrom,
		}); err != nil {
			form.Error = workspaceFormError(err)
		} else {
			http.Redirect(w, r, "/workspaces", http.StatusSeeOther)
			return
		}
	}
	if form.Error != "" {
		templates, _ := s.workspace.ListAvailableTemplates(r.Context(), user.ID)
		volumeOptions, directoryOptions := s.reattachOptionsForUser(r.Context(), user.ID)
		data := pageData{Title: "Create workspace | COWS", User: user, Templates: templates, CSRFToken: s.ensureCSRF(w, r), Workspace: form,
			ReattachVolumeOptions: volumeOptions, ReattachDirectoryOptions: directoryOptions}
		s.setWorkspaceAvailability(r.Context(), user.ID, &data)
		s.render(w, http.StatusBadRequest, "workspace-new-page", data)
		return
	}
}

// reattachVolumeSelections extracts the workspace-creation form's per-mount
// reattachment choices. Fields use the reattach_volume__<mountName> naming
// convention (mount names are restricted to a validated character set by
// template validation, so embedding one directly in a field name is safe);
// a blank selection means "start empty" and is dropped rather than kept as
// an empty override.
func reattachVolumeSelections(form url.Values) map[string]string {
	const prefix = "reattach_volume__"
	selections := make(map[string]string)
	for key, values := range form {
		if !strings.HasPrefix(key, prefix) || len(values) == 0 {
			continue
		}
		value := strings.TrimSpace(values[0])
		if value == "" {
			continue
		}
		selections[strings.TrimPrefix(key, prefix)] = value
	}
	return selections
}

func (s *Server) setWorkspaceAvailability(ctx context.Context, userID string, data *pageData) {
	available, err := s.workspace.ResourceAvailability(ctx, userID)
	if err != nil {
		return
	}
	data.WorkspaceAvailabilityKnown = true
	data.WorkspaceAvailableCPUMillis = available.CPUMillis
	data.WorkspaceAvailableMemoryMiB = available.MemoryBytes / (1 << 20)
}

func (s *Server) workspaceStart(w http.ResponseWriter, r *http.Request) {
	s.workspaceAction(w, r, "start", s.workspace.StartWorkspace)
}

func (s *Server) workspaceStop(w http.ResponseWriter, r *http.Request) {
	s.workspaceAction(w, r, "stop", s.workspace.StopWorkspace)
}

func (s *Server) workspaceRestart(w http.ResponseWriter, r *http.Request) {
	s.workspaceAction(w, r, "restart", s.workspace.RestartWorkspace)
}

func (s *Server) workspaceDelete(w http.ResponseWriter, r *http.Request) {
	s.workspaceAction(w, r, "delete", s.workspace.DeleteWorkspace)
}

func (s *Server) workspaceAction(w http.ResponseWriter, r *http.Request, action string, operation func(context.Context, string, string) error) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	workspaceID := r.PathValue("id")
	actionContext := classifyWorkspaceActionContext(r, user)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	opErr := operation(r.Context(), user.ID, workspaceID)
	if isHTMXRequest(r) {
		switch actionContext {
		case workspaceActionContextList:
			s.renderWorkspaceRow(w, r, user, workspaceID, action, opErr)
			return
		case workspaceActionContextDetail:
			s.renderWorkspaceDetailHeader(w, r, user, workspaceID, action, opErr)
			return
		case workspaceActionContextAdminRuntime:
			s.renderAdminRuntimeRow(w, r, user, workspaceID, action, opErr)
			return
		}
	}
	if opErr != nil {
		data, _ := s.workspacePageData(r.Context(), user)
		data.Title = "Workspaces | COWS"
		data.User = user
		data.CSRFToken = s.ensureCSRF(w, r)
		data.Error = workspaceActionError(action, opErr)
		s.render(w, http.StatusBadRequest, "workspaces-page", data)
		return
	}
	http.Redirect(w, r, actionContext.redirectPath(workspaceID), http.StatusSeeOther)
}

// isHTMXRequest reports whether htmx issued r (it always sends this header
// on requests it makes), distinguishing a dynamic row-action request from a
// plain form submission (e.g. JavaScript disabled).
func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// renderWorkspaceRow answers a row-action request with just the affected
// <tr>, so htmx can swap it in without a full page reload. A workspace that
// no longer exists (successful delete, or a concurrent delete from another
// tab) renders as an empty response, which removes the row entirely.
func (s *Server) renderWorkspaceRow(w http.ResponseWriter, r *http.Request, user *domain.User, workspaceID, action string, opErr error) {
	if opErr != nil && errors.Is(opErr, repository.ErrNotFound) {
		s.writeRemovedWorkspaceRow(r.Context(), w, user.ID)
		return
	}
	value, err := s.workspace.GetWorkspaceWithUsage(r.Context(), user.ID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		s.writeRemovedWorkspaceRow(r.Context(), w, user.ID)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace", http.StatusInternalServerError)
		return
	}
	access := workspaceAccess{}
	if methods, methodsErr := s.workspace.WorkspaceAccessMethods(r.Context(), user.ID, workspaceID); methodsErr == nil {
		for _, method := range methods {
			switch method {
			case domain.AccessTerminal:
				access.Terminal = true
			case domain.AccessDesktop:
				access.Desktop = true
			case domain.AccessFiles:
				access.Files = true
			case domain.AccessLogs:
				access.Logs = true
			}
		}
	}
	rowError := ""
	if opErr != nil {
		rowError = workspaceActionError(action, opErr)
	}
	row := workspaceRowData{Workspace: value, Access: access, CSRFToken: s.ensureCSRF(w, r), RowError: rowError}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "workspace-row", row)
	s.writeResourceStripOOB(r.Context(), w, user.ID)
}

// writeRemovedWorkspaceRow answers a row action whose workspace is gone
// (successful delete, or a concurrent delete from another tab). The empty
// row content removes the row; the out-of-band strip still ships, because
// a delete changes the totals it reports.
func (s *Server) writeRemovedWorkspaceRow(ctx context.Context, w http.ResponseWriter, userID string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	s.writeResourceStripOOB(ctx, w, userID)
}

// renderWorkspaceDetailHeader answers a Detail-page lifecycle action with
// just the header block (badge, name, status, meta, buttons), so htmx can
// swap it in without a full page reload. Unlike a list row, there is no
// empty-fragment-removes-the-row equivalent here: if the workspace no
// longer exists (successful delete, or a concurrent delete from another
// tab), an empty header would just leave the user staring at a broken
// page, so it sends HX-Redirect back to the Overview instead.
func (s *Server) renderWorkspaceDetailHeader(w http.ResponseWriter, r *http.Request, user *domain.User, workspaceID, action string, opErr error) {
	if opErr != nil && errors.Is(opErr, repository.ErrNotFound) {
		w.Header().Set("HX-Redirect", "/workspaces")
		w.WriteHeader(http.StatusOK)
		return
	}
	value, err := s.workspace.GetWorkspaceWithUsage(r.Context(), user.ID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		w.Header().Set("HX-Redirect", "/workspaces")
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace", http.StatusInternalServerError)
		return
	}
	access := workspaceAccess{}
	if methods, methodsErr := s.workspace.WorkspaceAccessMethods(r.Context(), user.ID, workspaceID); methodsErr == nil {
		for _, method := range methods {
			switch method {
			case domain.AccessTerminal:
				access.Terminal = true
			case domain.AccessDesktop:
				access.Desktop = true
			case domain.AccessFiles:
				access.Files = true
			case domain.AccessLogs:
				access.Logs = true
			}
		}
	}
	rowError := ""
	if opErr != nil {
		rowError = workspaceActionError(action, opErr)
	}
	header := workspaceDetailHeaderData{
		Workspace: value,
		Access:    access,
		CSRFToken: s.ensureCSRF(w, r),
		OwnerName: s.resolveWorkspaceOwnerName(r.Context(), user, value),
		RowError:  rowError,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "workspace-detail-header", header)
}

// resolveWorkspaceOwnerName renders the Detail page's "created ... by
// <owner>" line. For a user viewing their own workspace this is free (no
// query); an administrator viewing another user's workspace needs one
// best-effort lookup, and never fails the page over a display-only field.
func (s *Server) resolveWorkspaceOwnerName(ctx context.Context, viewer *domain.User, value domain.Workspace) string {
	if viewer != nil && value.OwnerUserID == viewer.ID {
		return viewer.Username
	}
	if viewer == nil {
		return value.OwnerUserID
	}
	owner, err := s.auth.FindUserForAdmin(ctx, viewer.ID, value.OwnerUserID)
	if err != nil {
		return value.OwnerUserID
	}
	return owner.Username
}

// workspaceActionContext classifies which surface issued a lifecycle
// action request, so the handler knows whether to answer with a table-row
// fragment, a detail-page header fragment, or a classic redirect. Browser
// input is never used as a raw redirect target - each context maps to a
// path computed here, never one supplied by the client.
type workspaceActionContext string

const (
	workspaceActionContextList         workspaceActionContext = "list"
	workspaceActionContextAdminRuntime workspaceActionContext = "admin-runtime"
	workspaceActionContextDetail       workspaceActionContext = "detail"
)

func classifyWorkspaceActionContext(r *http.Request, user *domain.User) workspaceActionContext {
	if user != nil && user.IsAdministrator() && r.FormValue("return_to") == "/admin/runtime" {
		return workspaceActionContextAdminRuntime
	}
	if r.FormValue("context") == "detail" {
		return workspaceActionContextDetail
	}
	return workspaceActionContextList
}

func (c workspaceActionContext) redirectPath(workspaceID string) string {
	switch c {
	case workspaceActionContextAdminRuntime:
		return "/admin/runtime"
	case workspaceActionContextDetail:
		return "/workspaces/" + workspaceID + "/access"
	default:
		return "/workspaces"
	}
}

func (s *Server) workspaceTerminalPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/workspaces/"+r.PathValue("id")+"/access?tab=terminal", http.StatusSeeOther)
}

func (s *Server) workspaceAccessPage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	data, err := s.workspaceAccessPageData(r.Context(), user, r.PathValue("id"), r.URL.Query().Get("tab"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "workspace access is not available", http.StatusForbidden)
		return
	}
	data.Title = "Workspace access | COWS"
	data.User = user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "workspace-access-page", data)
}

func (s *Server) workspaceAccessPageData(ctx context.Context, user *domain.User, workspaceID, requestedTab string) (pageData, error) {
	value, err := s.workspace.GetWorkspaceWithUsage(ctx, user.ID, workspaceID)
	if err != nil {
		return pageData{}, err
	}
	methods, err := s.workspace.WorkspaceAccessMethods(ctx, user.ID, workspaceID)
	if err != nil {
		return pageData{}, err
	}
	access := workspaceAccess{}
	for _, method := range methods {
		switch method {
		case domain.AccessTerminal:
			access.Terminal = true
		case domain.AccessDesktop:
			access.Desktop = true
		case domain.AccessFiles:
			access.Files = true
		case domain.AccessLogs:
			access.Logs = true
		}
	}
	tab := requestedTab
	if (tab == "terminal" && !access.Terminal) || (tab == "desktop" && !access.Desktop) || (tab == "files" && !access.Files) || (tab == "logs" && !access.Logs) || (tab != "terminal" && tab != "desktop" && tab != "files" && tab != "logs") {
		tab = firstAccessTab(access)
	}
	if tab == "" {
		return pageData{}, errors.New("workspace has no enabled access methods")
	}
	return pageData{
		ActiveWorkspace:    &value,
		WorkspaceOwnerName: s.resolveWorkspaceOwnerName(ctx, user, value),
		AccessTab:          tab,
		Access:             access,
		AccessFilesURL:     "/workspaces/" + workspaceID + "/access/files",
		AccessLogsURL:      "/workspaces/" + workspaceID + "/access/logs",
	}, nil
}

func firstAccessTab(access workspaceAccess) string {
	if access.Terminal {
		return "terminal"
	}
	if access.Desktop {
		return "desktop"
	}
	if access.Files {
		return "files"
	}
	if access.Logs {
		return "logs"
	}
	return ""
}

func (s *Server) workspaceAccessFilesFragment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	workspaceID := r.PathValue("id")
	mountName := r.URL.Query().Get("mount")
	relativePath := r.URL.Query().Get("path")
	listing, err := s.files.List(r.Context(), user.ID, workspaceID, mountName, relativePath)
	if err != nil {
		s.renderFileAccessFragmentError(w, r, user, workspaceID, mountName, relativePath, err)
		return
	}
	mounts, err := s.files.Mounts(r.Context(), user.ID, workspaceID)
	if err != nil {
		s.renderFileAccessFragmentError(w, r, user, workspaceID, mountName, relativePath, err)
		return
	}
	s.render(w, http.StatusOK, "workspace-files-panel", pageData{User: user, CSRFToken: s.ensureCSRF(w, r), ActiveWorkspace: &domain.Workspace{ID: workspaceID}, FileListing: &listing, FileMounts: mounts})
}

// workspaceAccessLogsFragment answers the Logs tab's lazy-load fetch (and
// its own Refresh button) with a rendered fragment of the container's own
// stdout/stderr - a bounded tail, not a live stream. Access is gated the
// same way Files is: the workspace must belong to the requester (or the
// requester must be an administrator, via GetWorkspace), and the template
// must have Logs enabled as an access method.
func (s *Server) workspaceAccessLogsFragment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	workspaceID := r.PathValue("id")
	value, err := s.workspace.GetWorkspace(r.Context(), user.ID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "workspace is not available", http.StatusForbidden)
		return
	}
	methods, err := s.workspace.WorkspaceAccessMethods(r.Context(), user.ID, workspaceID)
	logsEnabled := false
	for _, method := range methods {
		if method == domain.AccessLogs {
			logsEnabled = true
			break
		}
	}
	if err != nil || !logsEnabled {
		http.Error(w, "log access is not enabled for this workspace", http.StatusForbidden)
		return
	}
	data := pageData{AccessLogsURL: "/workspaces/" + workspaceID + "/access/logs"}
	logsRuntime, runtimeOK := s.runtime.(runtime.LogsRuntime)
	switch {
	case !runtimeOK:
		data.LogError = "Log viewing is not supported by the configured runtime."
	case value.RuntimeID == "":
		data.LogError = "This workspace has no container yet."
	default:
		lines, logsErr := logsRuntime.WorkspaceLogs(r.Context(), value.RuntimeID, 200)
		if logsErr != nil {
			data.LogError = "The container logs could not be read."
		} else {
			data.LogLines = lines
		}
	}
	s.render(w, http.StatusOK, "workspace-logs-panel", data)
}

// workspaceAccessHeaderFragment answers the Detail page header's own
// periodic poll (it re-fetches itself every 10s, same as
// renderWorkspaceDetailHeader's action-triggered swap) so the status dot
// reflects a state change from elsewhere (another tab, an administrator, a
// timeout-triggered auto-stop) without the user reloading the page.
func (s *Server) workspaceAccessHeaderFragment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	s.renderWorkspaceDetailHeader(w, r, user, r.PathValue("id"), "", nil)
}

// workspaceAccessSidebarFragment answers the Detail page sidebar's own
// periodic poll. The Resources card in particular is otherwise a one-time
// snapshot from page load - live CPU/memory numbers would freeze at
// whatever they were when the page was fetched (often 0% if the container
// was idle at that instant) instead of tracking the workspace while the
// page stays open.
func (s *Server) workspaceAccessSidebarFragment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	workspaceID := r.PathValue("id")
	value, err := s.workspace.GetWorkspaceWithUsage(r.Context(), user.ID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "workspace-detail-sidebar", value)
}

type terminalResizeMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type terminalInputMessage struct {
	Type websocket.MessageType
	Data []byte
	Err  error
}

type terminalOutputMessage struct {
	Data []byte
	Err  error
}

func (s *Server) workspaceTerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	requestedUID, err := parseTerminalUID(r.URL.Query().Get("uid"))
	if err != nil {
		http.Error(w, "The requested terminal identity is invalid.", http.StatusBadRequest)
		return
	}
	terminal, err := s.workspace.OpenTerminal(r.Context(), user.ID, r.PathValue("id"), requestedUID)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, workspace.ErrWorkspaceNotAuthorized) || errors.Is(err, workspace.ErrTerminalNotAvailable) || errors.Is(err, workspace.ErrTerminalIdentityNotAllowed) {
			status = http.StatusForbidden
		}
		http.Error(w, terminalErrorText(err), status)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.closeTerminalSession(terminal, user.ID, r.PathValue("id"))
		s.workspace.RecordTerminalDisconnect(context.Background(), user.ID, r.PathValue("id"))
		return
	}
	defer func() {
		conn.CloseNow()
		// Close the runtime terminal before recording the audit event. The
		// cleanup is authoritative; the audit write must not delay it.
		s.closeTerminalSession(terminal, user.ID, r.PathValue("id"))
		s.workspace.RecordTerminalDisconnect(context.Background(), user.ID, r.PathValue("id"))
	}()
	conn.SetReadLimit(64 * 1024)

	ctx, cancel := context.WithTimeout(r.Context(), terminalMaxLifetime)
	defer cancel()
	sessionCookie, err := r.Cookie(sessionCookieName)
	if err != nil || sessionCookie.Value == "" {
		return
	}
	input := make(chan terminalInputMessage, 1)
	output := make(chan terminalOutputMessage, 1)
	go readTerminalInput(ctx, conn, input)
	go readTerminalOutput(ctx, terminal, output)

	idleTimer := time.NewTimer(terminalIdleTimeout)
	defer idleTimer.Stop()
	sessionTicker := time.NewTicker(terminalSessionCheckInterval)
	defer sessionTicker.Stop()
	resetIdle := func() {
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(terminalIdleTimeout)
	}
	for {
		select {
		case message := <-output:
			if message.Err != nil {
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, message.Data); err != nil {
				return
			}
			resetIdle()
		case message := <-input:
			if message.Err != nil {
				return
			}
			var resize terminalResizeMessage
			if message.Type == websocket.MessageText && json.Unmarshal(message.Data, &resize) == nil && resize.Type == "resize" {
				// Resize is best-effort: the adapter rejects out-of-range cols/rows
				// with ErrConflict. Ignore validation failures instead of dropping
				// the terminal session, but keep returning on genuine runtime errors.
				if err := terminal.Resize(ctx, resize.Cols, resize.Rows); err != nil && !errors.Is(err, runtime.ErrConflict) {
					return
				}
				resetIdle()
				continue
			}
			if _, err := terminal.Write(message.Data); err != nil {
				return
			}
			resetIdle()
		case <-idleTimer.C:
			return
		case <-ctx.Done():
			return
		case <-sessionTicker.C:
			current, err := s.auth.UserForSession(ctx, sessionCookie.Value)
			if err != nil || current.ID != user.ID || current.Disabled || current.MustChangePassword {
				return
			}
		}
	}
}

func (s *Server) closeTerminalSession(terminal runtime.Terminal, userID, workspaceID string) {
	if err := terminal.Close(); err != nil {
		s.logger.Error("terminal cleanup failed", "user_id", userID, "workspace_id", workspaceID, "error", err)
	}
}

func parseTerminalUID(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	uid, err := strconv.ParseInt(value, 10, 64)
	if err != nil || uid < 0 || uid > 2147483647 {
		return nil, errors.New("invalid terminal UID")
	}
	return &uid, nil
}

func readTerminalInput(ctx context.Context, conn *websocket.Conn, output chan<- terminalInputMessage) {
	for {
		typ, data, err := conn.Read(ctx)
		select {
		case output <- terminalInputMessage{Type: typ, Data: data, Err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func readTerminalOutput(ctx context.Context, terminal runtime.Terminal, output chan<- terminalOutputMessage) {
	buffer := make([]byte, 32*1024)
	for {
		count, err := terminal.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			select {
			case output <- terminalOutputMessage{Data: data}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			select {
			case output <- terminalOutputMessage{Err: err}:
			case <-ctx.Done():
			}
			return
		}
	}
}

func (s *Server) workspaceDesktopPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/workspaces/"+r.PathValue("id")+"/access?tab=desktop", http.StatusSeeOther)
}

func (s *Server) workspaceDesktopCredentials(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	password, err := s.workspace.GetDesktopCredentials(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, desktopErrorText(err), status)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		Password string `json:"password"`
	}{Password: password})
}

func (s *Server) workspaceDesktopWebSocket(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	desktop, err := s.workspace.OpenDesktop(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, workspace.ErrWorkspaceNotAuthorized) || errors.Is(err, workspace.ErrDesktopNotAvailable) {
			status = http.StatusForbidden
		}
		http.Error(w, desktopErrorText(err), status)
		return
	}
	defer desktop.Close()
	defer s.workspace.RecordDesktopDisconnect(context.Background(), user.ID, r.PathValue("id"))

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	sessionCookie, err := r.Cookie(sessionCookieName)
	if err != nil || sessionCookie.Value == "" {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), terminalMaxLifetime)
	defer cancel()
	browser := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	defer browser.Close()

	lastActivity := &atomic.Int64{}
	lastActivity.Store(time.Now().UnixNano())
	browserStream := &activityStream{ReadWriteCloser: browser, lastActivity: lastActivity}
	desktopStream := &activityStream{ReadWriteCloser: desktop, lastActivity: lastActivity}
	done := make(chan error, 2)
	go copyDesktopStream(desktopStream, browserStream, done)
	go copyDesktopStream(browserStream, desktopStream, done)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	sessionTicker := time.NewTicker(terminalSessionCheckInterval)
	defer sessionTicker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, lastActivity.Load())) >= terminalIdleTimeout {
				return
			}
		case <-sessionTicker.C:
			current, sessionErr := s.auth.UserForSession(ctx, sessionCookie.Value)
			if sessionErr != nil || current.ID != user.ID || current.Disabled || current.MustChangePassword {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) workspaceFilesPage(w http.ResponseWriter, r *http.Request) {
	values := url.Values{"tab": {"files"}}
	if mount := r.URL.Query().Get("mount"); mount != "" {
		values.Set("mount", mount)
	}
	if relativePath := r.URL.Query().Get("path"); relativePath != "" {
		values.Set("path", relativePath)
	}
	http.Redirect(w, r, "/workspaces/"+r.PathValue("id")+"/access?"+values.Encode(), http.StatusSeeOther)
}

func (s *Server) workspaceFileDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	file, info, err := s.files.OpenDownload(r.Context(), user.ID, r.PathValue("id"), r.URL.Query().Get("mount"), r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, fileErrorText(err), fileErrorStatus(err))
		return
	}
	defer file.Close()
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(path.Base(r.URL.Query().Get("path"))))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if info.Size() >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	if !info.ModTime().IsZero() {
		w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	}
	_, _ = io.Copy(w, file)
}

func (s *Server) workspaceFileDownloadZip(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	archive, filename, err := s.files.OpenZip(r.Context(), user.ID, r.PathValue("id"), r.URL.Query().Get("mount"), r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, fileErrorText(err), fileErrorStatus(err))
		return
	}
	defer archive.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filename))
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, archive); err != nil {
		return
	}
}

func (s *Server) workspaceFileMkdir(w http.ResponseWriter, r *http.Request) {
	s.workspaceFileMutation(w, r, func(user *domain.User, workspaceID string) error {
		return s.files.CreateDirectory(r.Context(), user.ID, workspaceID, r.FormValue("mount"), r.FormValue("path"), r.FormValue("name"))
	})
}

func (s *Server) workspaceFileDelete(w http.ResponseWriter, r *http.Request) {
	s.workspaceFileMutationTo(w, r, func(user *domain.User, workspaceID string) error {
		return s.files.Delete(r.Context(), user.ID, workspaceID, r.FormValue("mount"), r.FormValue("path"))
	}, fileDeleteParent)
}

func fileDeleteParent(relativePath string) string {
	return path.Dir(relativePath)
}

func (s *Server) workspaceFileRename(w http.ResponseWriter, r *http.Request) {
	s.workspaceFileMutation(w, r, func(user *domain.User, workspaceID string) error {
		return s.files.Rename(r.Context(), user.ID, workspaceID, r.FormValue("mount"), r.FormValue("path"), r.FormValue("old_name"), r.FormValue("new_name"))
	})
}

func (s *Server) workspaceFileUpload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, files.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	upload, header, err := r.FormFile("file")
	if err != nil {
		s.renderFilePageError(w, r, user, r.PathValue("id"), r.FormValue("mount"), r.FormValue("path"), err)
		return
	}
	defer upload.Close()
	if err := s.files.Upload(r.Context(), user.ID, r.PathValue("id"), r.FormValue("mount"), r.FormValue("path"), header.Filename, upload); err != nil {
		s.renderFilePageError(w, r, user, r.PathValue("id"), r.FormValue("mount"), r.FormValue("path"), err)
		return
	}
	s.redirectFiles(w, r, r.PathValue("id"), r.FormValue("mount"), r.FormValue("path"))
}

// storagePage lists the current user's own retained volumes and archived
// directories (self-service storage, decision 0025). This is deliberately a
// separate route and handler from /admin/volumes: that page lists every
// user's retained storage for administrators, this one is owner-scoped by
// query, and the two must never share a code path where a future edit could
// blur the authorization boundary between them.
func (s *Server) storagePage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	csrfToken := s.ensureCSRF(w, r)
	data := pageData{Title: "My storage | COWS", User: user, CSRFToken: csrfToken}
	if s.options.Store != nil {
		if volumes, err := s.options.Store.ListRetainedWorkspaceVolumesForOwner(r.Context(), user.ID); err == nil {
			volumeRuntime, _ := s.runtime.(runtime.VolumeRuntime)
			views := make([]retainedVolumeView, 0, len(volumes))
			for _, volume := range volumes {
				view := retainedVolumeView{RetainedWorkspaceVolume: volume, CSRFToken: csrfToken}
				if volumeRuntime != nil {
					view.RuntimePresent, _ = volumeRuntime.VolumeExists(r.Context(), volume.VolumeName)
				}
				views = append(views, view)
			}
			data.MyRetainedVolumes = views
		}
		if directories, err := s.options.Store.ListRetainedWorkspaceDirectoriesForOwner(r.Context(), user.ID); err == nil {
			data.MyRetainedDirectories = directories
		}
	}
	s.render(w, http.StatusOK, "storage-page", data)
}

func (s *Server) recordStorageAudit(ctx context.Context, actorID, eventType, targetType, targetID string, metadata map[string]string) {
	if s.options.Store != nil {
		_ = s.options.Store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: eventType, TargetType: targetType, TargetID: targetID, Metadata: metadata, CreatedAt: time.Now().UTC()})
	}
}

func (s *Server) storageVolumeDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if s.options.Store == nil {
		http.Error(w, "volume metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	workspaceID, mountName := r.PathValue("workspace_id"), r.PathValue("mount_name")
	volume, err := s.options.Store.FindRetainedWorkspaceVolume(r.Context(), workspaceID, mountName, user.ID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained volume", http.StatusInternalServerError)
		return
	}
	reader, err := s.files.OpenRuntimeZip(r.Context(), runtime.FileAccessSpec{MountType: "volume", Source: volume.VolumeName, ContainerPath: volume.ContainerPath, ContainerUID: 0, ContainerGID: 0, ReadOnly: true}, volume.MountName)
	if err != nil {
		http.Error(w, "the retained volume could not be read", http.StatusServiceUnavailable)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+volume.MountName+`.zip"`)
	s.recordStorageAudit(r.Context(), user.ID, "storage.volume_downloaded", "volume", volume.VolumeName, map[string]string{"workspace_id": volume.WorkspaceID, "mount_name": volume.MountName})
	_, _ = io.Copy(w, reader)
}

func (s *Server) storageVolumeDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if s.options.Store == nil {
		http.Error(w, "volume metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	workspaceID, mountName := r.PathValue("workspace_id"), r.PathValue("mount_name")
	volume, err := s.options.Store.FindRetainedWorkspaceVolume(r.Context(), workspaceID, mountName, user.ID)
	if errors.Is(err, repository.ErrNotFound) {
		if isHTMXRequest(r) {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained volume", http.StatusInternalServerError)
		return
	}
	// Remove the real volume before consuming its tombstone: if removal
	// fails, the tombstone must survive so the volume stays discoverable
	// and the user can retry, instead of silently leaking it while
	// reporting success (mirrors the already-correct ordering in
	// adminVolumeDelete).
	if volumeRuntime, ok := s.runtime.(runtime.VolumeRuntime); ok {
		if err := volumeRuntime.RemoveVolume(r.Context(), volume.VolumeName); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			http.Error(w, "the retained volume could not be removed", http.StatusServiceUnavailable)
			return
		}
	}
	if _, err := s.options.Store.ConsumeRetainedWorkspaceVolume(r.Context(), workspaceID, mountName, user.ID); err != nil && !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, repository.ErrConflict) {
		http.Error(w, "failed to remove retained volume", http.StatusInternalServerError)
		return
	}
	s.recordStorageAudit(r.Context(), user.ID, "storage.volume_deleted", "volume", volume.VolumeName, map[string]string{"workspace_id": volume.WorkspaceID, "mount_name": volume.MountName})
	if isHTMXRequest(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/storage", http.StatusSeeOther)
}

func (s *Server) storageDirectoryDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	workspaceID := r.PathValue("workspace_id")
	reader, name, err := s.workspace.OpenRetainedDirectoryZip(r.Context(), user.ID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "the retained directory could not be read", http.StatusServiceUnavailable)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
	s.recordStorageAudit(r.Context(), user.ID, "storage.directory_downloaded", "directory", workspaceID, map[string]string{"workspace_id": workspaceID})
	_, _ = io.Copy(w, reader)
}

func (s *Server) storageDirectoryDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	workspaceID := r.PathValue("workspace_id")
	if err := s.workspace.DeleteRetainedDirectory(r.Context(), user.ID, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrConflict) {
			if isHTMXRequest(r) {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to remove retained directory", http.StatusInternalServerError)
		return
	}
	s.recordStorageAudit(r.Context(), user.ID, "storage.directory_deleted", "directory", workspaceID, map[string]string{"workspace_id": workspaceID})
	if isHTMXRequest(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/storage", http.StatusSeeOther)
}

func (s *Server) workspaceFileMutation(w http.ResponseWriter, r *http.Request, action func(*domain.User, string) error) {
	s.workspaceFileMutationTo(w, r, action, func(relativePath string) string { return relativePath })
}

func (s *Server) workspaceFileMutationTo(w http.ResponseWriter, r *http.Request, action func(*domain.User, string) error, redirectPath func(string) string) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if err := action(user, r.PathValue("id")); err != nil {
		s.renderFilePageError(w, r, user, r.PathValue("id"), r.FormValue("mount"), r.FormValue("path"), err)
		return
	}
	s.redirectFiles(w, r, r.PathValue("id"), r.FormValue("mount"), redirectPath(r.FormValue("path")))
}

func (s *Server) redirectFiles(w http.ResponseWriter, r *http.Request, workspaceID, mountName, relativePath string) {
	values := url.Values{}
	values.Set("mount", mountName)
	values.Set("path", relativePath)
	values.Set("tab", "files")
	http.Redirect(w, r, "/workspaces/"+workspaceID+"/access?"+values.Encode(), http.StatusSeeOther)
}

func (s *Server) renderFilePageError(w http.ResponseWriter, r *http.Request, user *domain.User, workspaceID, mountName, relativePath string, err error) {
	mounts, _ := s.files.Mounts(r.Context(), user.ID, workspaceID)
	listing, _ := s.files.List(r.Context(), user.ID, workspaceID, mountName, relativePath)
	data, accessErr := s.workspaceAccessPageData(r.Context(), user, workspaceID, "files")
	if accessErr != nil {
		http.Error(w, fileErrorText(err), fileErrorStatus(err))
		return
	}
	data.Title = "Workspace access | COWS"
	data.User = user
	data.CSRFToken = s.ensureCSRF(w, r)
	data.FileListing = &listing
	data.FileMounts = mounts
	data.FileError = fileErrorText(err)
	s.render(w, fileErrorStatus(err), "workspace-access-page", data)
}

func (s *Server) renderFileAccessFragmentError(w http.ResponseWriter, r *http.Request, user *domain.User, workspaceID, mountName, relativePath string, err error) {
	mounts, _ := s.files.Mounts(r.Context(), user.ID, workspaceID)
	listing, _ := s.files.List(r.Context(), user.ID, workspaceID, mountName, relativePath)
	s.render(w, fileErrorStatus(err), "workspace-files-panel", pageData{User: user, CSRFToken: s.ensureCSRF(w, r), ActiveWorkspace: &domain.Workspace{ID: workspaceID}, FileListing: &listing, FileMounts: mounts, FileError: fileErrorText(err)})
}

type activityStream struct {
	io.ReadWriteCloser
	lastActivity *atomic.Int64
}

func (s *activityStream) Read(value []byte) (int, error) {
	count, err := s.ReadWriteCloser.Read(value)
	if count > 0 {
		s.lastActivity.Store(time.Now().UnixNano())
	}
	return count, err
}

func (s *activityStream) Write(value []byte) (int, error) {
	count, err := s.ReadWriteCloser.Write(value)
	if count > 0 {
		s.lastActivity.Store(time.Now().UnixNano())
	}
	return count, err
}

func copyDesktopStream(destination io.Writer, source io.Reader, done chan<- error) {
	_, err := io.Copy(destination, source)
	done <- err
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
	groups := s.templateGroups(r.Context(), user.ID)
	groupByID := make(map[string]domain.Group, len(groups))
	for _, group := range groups {
		groupByID[group.ID] = group
	}
	userGroupIDs := make(map[string][]string, len(users))
	userGroups := make(map[string][]domain.Group, len(users))
	for _, value := range users {
		ids, groupErr := s.auth.UserGroupIDs(r.Context(), user.ID, value.ID)
		if groupErr != nil {
			http.Error(w, "failed to load user groups", http.StatusInternalServerError)
			return
		}
		userGroupIDs[value.ID] = ids
		for _, groupID := range ids {
			if group, exists := groupByID[groupID]; exists {
				userGroups[value.ID] = append(userGroups[value.ID], group)
			}
		}
	}
	s.render(w, http.StatusOK, "admin-users-page", pageData{
		Title:        "Users | COWS",
		User:         &user,
		Users:        users,
		Groups:       groups,
		UserGroupIDs: userGroupIDs,
		UserGroups:   userGroups,
		CSRFToken:    s.ensureCSRF(w, r),
	})
}

func (s *Server) adminUserEdit(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	data, err := s.userEditPageData(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load user", http.StatusInternalServerError)
		return
	}
	data.Title = "Edit user | COWS"
	data.User = &user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "admin-user-edit-page", data)
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
	form := userFormData{Username: r.FormValue("username"), Email: r.FormValue("email"), DisplayName: r.FormValue("display_name"), Role: r.FormValue("role")}
	_, err := s.auth.CreateUser(r.Context(), user.ID, auth.CreateUserInput{
		Username:    form.Username,
		Email:       form.Email,
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
	targetID := r.PathValue("id")
	notFound := false
	cleanupFailed := false
	rowErrorText := ""
	if err := s.auth.SetUserDisabled(r.Context(), user.ID, targetID, disabled); err != nil {
		notFound = errors.Is(err, repository.ErrNotFound)
		rowErrorText = userActionError(err)
	} else if disabled {
		if err := s.workspace.StopUserWorkspaces(r.Context(), user.ID, targetID); err != nil {
			cleanupFailed = true
			rowErrorText = "The user was disabled and all sessions were invalidated, but one or more workspaces could not be stopped. Check Runtime and retry."
		}
	}
	if isHTMXRequest(r) {
		s.renderUserRow(w, r, user, targetID, rowErrorText, notFound)
		return
	}
	switch {
	case notFound:
		http.Error(w, rowErrorText, http.StatusNotFound)
	case cleanupFailed:
		http.Error(w, rowErrorText, http.StatusServiceUnavailable)
	case rowErrorText != "":
		http.Error(w, rowErrorText, http.StatusBadRequest)
	default:
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// renderUserRow answers a row-action request with just the affected <tr>.
// removed indicates the target no longer exists (deleted, or not found),
// in which case the response is empty and htmx removes the row.
func (s *Server) renderUserRow(w http.ResponseWriter, r *http.Request, actor domain.User, targetID, rowError string, removed bool) {
	if removed {
		w.WriteHeader(http.StatusOK)
		return
	}
	target, err := s.auth.FindUserForAdmin(r.Context(), actor.ID, targetID)
	if errors.Is(err, repository.ErrNotFound) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		http.Error(w, "failed to load user", http.StatusInternalServerError)
		return
	}
	groupIDs, err := s.auth.UserGroupIDs(r.Context(), actor.ID, targetID)
	var groups []domain.Group
	if err == nil {
		for _, group := range s.templateGroups(r.Context(), actor.ID) {
			for _, id := range groupIDs {
				if group.ID == id {
					groups = append(groups, group)
					break
				}
			}
		}
	}
	row := userRowData{Target: target, Groups: groups, CSRFToken: s.ensureCSRF(w, r), CurrentUserID: actor.ID, RowError: rowError}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "admin-user-row", row)
}

func (s *Server) adminUserDelete(w http.ResponseWriter, r *http.Request) {
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
	targetID := r.PathValue("id")
	var opErr error
	notFound := false
	serviceUnavailable := false
	if err := s.workspace.DeleteUserWorkspaces(r.Context(), user.ID, targetID); err != nil {
		opErr = err
		notFound = errors.Is(err, repository.ErrNotFound)
		serviceUnavailable = errors.Is(err, workspace.ErrRuntimeUnavailable) || errors.Is(err, workspace.ErrWorkspaceCleanupIncomplete)
	} else if err := s.auth.DeleteUser(r.Context(), user.ID, targetID); err != nil {
		opErr = err
		notFound = errors.Is(err, repository.ErrNotFound)
	}
	if isHTMXRequest(r) {
		rowErrorText := ""
		if opErr != nil {
			rowErrorText = userDeleteError(opErr)
		}
		s.renderUserRow(w, r, user, targetID, rowErrorText, opErr == nil || notFound)
		return
	}
	switch {
	case opErr == nil:
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	case notFound:
		http.Error(w, userDeleteError(opErr), http.StatusNotFound)
	case serviceUnavailable:
		http.Error(w, userDeleteError(opErr), http.StatusServiceUnavailable)
	default:
		http.Error(w, userDeleteError(opErr), http.StatusBadRequest)
	}
}

func (s *Server) adminUserGroups(w http.ResponseWriter, r *http.Request) {
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
	if err := s.auth.SetUserGroups(r.Context(), user.ID, r.PathValue("id"), r.Form["group_ids"]); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, userActionError(err), status)
		return
	}
	http.Redirect(w, r, "/admin/users/"+r.PathValue("id")+"/edit", http.StatusSeeOther)
}

func (s *Server) adminGroups(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	groups, err := s.auth.ListGroups(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "failed to load groups", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "admin-groups-page", pageData{Title: "Groups | COWS", User: &user, Groups: groups, CSRFToken: s.ensureCSRF(w, r)})
}

func (s *Server) adminGroupsCreate(w http.ResponseWriter, r *http.Request) {
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
	if _, err := s.auth.CreateGroup(r.Context(), user.ID, r.FormValue("name"), r.FormValue("description")); err != nil {
		groups, _ := s.auth.ListGroups(r.Context(), user.ID)
		s.render(w, http.StatusBadRequest, "admin-groups-page", pageData{Title: "Groups | COWS", User: &user, Groups: groups, CSRFToken: s.ensureCSRF(w, r), Error: "The group could not be created. Check the name and try again."})
		return
	}
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (s *Server) adminGroupDelete(w http.ResponseWriter, r *http.Request) {
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
	if err := s.auth.DeleteGroup(r.Context(), user.ID, r.PathValue("id")); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, groupActionError(err), status)
		return
	}
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (s *Server) adminGroupEdit(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	data, err := s.groupEditPageData(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load group", http.StatusInternalServerError)
		return
	}
	data.Title = "Edit group | COWS"
	data.User = &user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "admin-group-edit-page", data)
}

func (s *Server) adminQuotaUpdate(w http.ResponseWriter, r *http.Request) {
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
	form := quotaFormData{UserID: r.PathValue("id"), MaxCPUMillis: r.FormValue("max_cpu_millis"), MaxMemoryMiB: r.FormValue("max_memory_mib"), MaxStorageGiB: r.FormValue("max_storage_gib"), MaxWorkspaces: r.FormValue("max_workspaces"), MaxRunningWorkspaces: r.FormValue("max_running_workspaces")}
	input, err := parseQuotaForm(form)
	if err == nil {
		if s.quota == nil {
			err = errors.New("quota service unavailable")
		} else {
			_, err = s.quota.Set(r.Context(), user.ID, form.UserID, input)
		}
	}
	if err != nil {
		form.Error = quotaFormError(err)
		data, loadErr := s.userEditPageData(r.Context(), user.ID, form.UserID)
		if errors.Is(loadErr, repository.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if loadErr != nil {
			http.Error(w, "failed to load user", http.StatusInternalServerError)
			return
		}
		data.Title = "Edit user | COWS"
		data.User = &user
		data.Quota = form
		data.CSRFToken = s.ensureCSRF(w, r)
		s.render(w, http.StatusBadRequest, "admin-user-edit-page", data)
		return
	}
	http.Redirect(w, r, "/admin/users/"+form.UserID+"/edit", http.StatusSeeOther)
}

func (s *Server) adminQuotaDelete(w http.ResponseWriter, r *http.Request) {
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
	userID := r.PathValue("id")
	if s.quota == nil {
		http.Error(w, "quota service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.quota.Unset(r.Context(), user.ID, userID); err != nil {
		http.Error(w, "quota assignment could not be removed", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/users/"+userID+"/edit", http.StatusSeeOther)
}

func (s *Server) adminGroupQuotaUpdate(w http.ResponseWriter, r *http.Request) {
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
	form := quotaFormData{GroupID: r.PathValue("id"), MaxCPUMillis: r.FormValue("max_cpu_millis"), MaxMemoryMiB: r.FormValue("max_memory_mib"), MaxStorageGiB: r.FormValue("max_storage_gib"), MaxWorkspaces: r.FormValue("max_workspaces"), MaxRunningWorkspaces: r.FormValue("max_running_workspaces")}
	input, err := parseQuotaForm(form)
	if err == nil {
		if s.quota == nil {
			err = errors.New("quota service unavailable")
		} else {
			_, err = s.quota.SetGroup(r.Context(), user.ID, form.GroupID, input)
		}
	}
	if err != nil {
		form.Error = quotaFormError(err)
		data, loadErr := s.groupEditPageData(r.Context(), user.ID, form.GroupID)
		if errors.Is(loadErr, repository.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if loadErr != nil {
			http.Error(w, "failed to load group", http.StatusInternalServerError)
			return
		}
		data.Title = "Edit group | COWS"
		data.User = &user
		data.GroupQuota = form
		data.CSRFToken = s.ensureCSRF(w, r)
		s.render(w, http.StatusBadRequest, "admin-group-edit-page", data)
		return
	}
	http.Redirect(w, r, "/admin/groups/"+form.GroupID+"/edit", http.StatusSeeOther)
}

func (s *Server) adminGroupQuotaDelete(w http.ResponseWriter, r *http.Request) {
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
	groupID := r.PathValue("id")
	if s.quota == nil {
		http.Error(w, "quota service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.quota.UnsetGroup(r.Context(), user.ID, groupID); err != nil {
		http.Error(w, "group quota assignment could not be removed", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/groups/"+groupID+"/edit", http.StatusSeeOther)
}

func (s *Server) userEditPageData(ctx context.Context, actorID, targetID string) (pageData, error) {
	target, err := s.auth.FindUserForAdmin(ctx, actorID, targetID)
	if err != nil {
		return pageData{}, err
	}
	groupIDs, err := s.auth.UserGroupIDs(ctx, actorID, target.ID)
	if err != nil {
		return pageData{}, err
	}
	data := pageData{
		EditUser:       &target,
		Groups:         s.templateGroups(ctx, actorID),
		EditUserGroups: groupIDs,
		Quota:          quotaFormData{UserID: target.ID},
	}
	if s.quota == nil {
		return data, nil
	}
	assigned, err := s.quota.Get(ctx, actorID, target.ID)
	if errors.Is(err, repository.ErrNotFound) {
		return data, nil
	}
	if err != nil {
		return pageData{}, err
	}
	data.Quota = quotaFormFromUserQuota(assigned)
	data.QuotaAssigned = true
	return data, nil
}

func (s *Server) groupEditPageData(ctx context.Context, actorID, targetID string) (pageData, error) {
	target, err := s.auth.FindGroupForAdmin(ctx, actorID, targetID)
	if err != nil {
		return pageData{}, err
	}
	data := pageData{
		EditGroup:  &target,
		GroupQuota: quotaFormData{GroupID: target.ID},
	}
	if s.quota == nil {
		return data, nil
	}
	assigned, err := s.quota.GetGroup(ctx, actorID, target.ID)
	if errors.Is(err, repository.ErrNotFound) {
		return data, nil
	}
	if err != nil {
		return pageData{}, err
	}
	data.GroupQuota = quotaFormFromGroupQuota(assigned)
	data.GroupQuotaAssigned = true
	return data, nil
}

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	if s.quota == nil {
		http.Error(w, "quota service unavailable", http.StatusServiceUnavailable)
		return
	}
	settings, err := s.quota.GetHostSettings(r.Context(), user.ID)
	if errors.Is(err, repository.ErrNotFound) {
		http.Error(w, "host settings have not been initialized", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "failed to load host settings", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "admin-settings-page", pageData{
		Title: "Host settings | COWS", User: &user, Settings: &settings,
		SettingsForm: settingsFormFromDomain(settings), CSRFToken: s.ensureCSRF(w, r),
	})
}

func (s *Server) adminSettingsUpdate(w http.ResponseWriter, r *http.Request) {
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
	form := settingsFormData{
		HostStorageGiB: r.FormValue("host_storage_gib"), CPUOverbookingFactor: r.FormValue("cpu_overbooking_factor"),
		MemoryOverbookingFactor: r.FormValue("memory_overbooking_factor"), ReservedStorageGiB: r.FormValue("reserved_storage_gib"),
	}
	input, err := parseSettingsForm(form)
	if err == nil && s.quota == nil {
		err = errors.New("quota service unavailable")
	}
	if err == nil {
		_, err = s.quota.SetHostSettings(r.Context(), user.ID, input)
	}
	if err != nil {
		form.Error = settingsFormError(err)
		var settings *domain.HostSettings
		if s.quota != nil {
			if current, loadErr := s.quota.GetHostSettings(r.Context(), user.ID); loadErr == nil {
				settings = &current
			}
		}
		s.render(w, http.StatusBadRequest, "admin-settings-page", pageData{Title: "Host settings | COWS", User: &user, Settings: settings, SettingsForm: form, CSRFToken: s.ensureCSRF(w, r)})
		return
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
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
	csrfToken := s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "admin-templates-page", pageData{Title: "Templates | COWS", User: &user, Templates: templates, TemplateImages: s.templateImageViews(r.Context(), templates, csrfToken), CSRFToken: csrfToken})
}

func (s *Server) templateImageViews(ctx context.Context, templates []domain.WorkspaceTemplate, csrfToken string) []templateImageView {
	views := make([]templateImageView, 0, len(templates))
	for _, value := range templates {
		views = append(views, s.templateImageViewFor(ctx, value, csrfToken))
	}
	return views
}

func (s *Server) templateImageViewFor(ctx context.Context, value domain.WorkspaceTemplate, csrfToken string) templateImageView {
	imageRuntime, supported := s.runtime.(runtime.ImageRuntime)
	view := templateImageView{WorkspaceTemplate: value, CSRFToken: csrfToken, Status: "unavailable", Message: "Runtime image status unavailable."}
	image := runtime.Image{Reference: value.ImageReference, Digest: value.ImageDigest}
	if operation := s.imagePullSnapshot(value.ID); operation != nil && operation.Reference == imageReference(image) {
		view.Status = operation.Status
		view.Message = operation.Message
		view.Pulling = operation.Status == "pulling"
	} else if supported {
		available, err := imageRuntime.ImageAvailable(ctx, image)
		switch {
		case err != nil:
			view.Status = "unavailable"
			view.Message = "Runtime image status unavailable."
		case available:
			view.Status = "available"
			view.Message = "Available on this system."
		default:
			view.Status = "missing"
			view.Message = "Not available on this system. Pull it before creating a workspace."
		}
	}
	return view
}

func imageReference(image runtime.Image) string {
	if image.Digest == "" {
		return image.Reference
	}
	return image.Reference + "@" + image.Digest
}

func (s *Server) imagePullSnapshot(templateID string) *imagePullOperation {
	s.imagePullMu.Lock()
	defer s.imagePullMu.Unlock()
	operation := s.imagePulls[templateID]
	if operation == nil {
		return nil
	}
	copy := *operation
	return &copy
}

func (s *Server) startImagePull(templateID string, image runtime.Image, imageRuntime runtime.ImageRuntime) *imagePullOperation {
	s.imagePullMu.Lock()
	if operation := s.imagePulls[templateID]; operation != nil && operation.Status == "pulling" {
		copy := *operation
		s.imagePullMu.Unlock()
		return &copy
	}
	operation := &imagePullOperation{Reference: imageReference(image), Status: "pulling", Message: "Pulling image..."}
	s.imagePulls[templateID] = operation
	copy := *operation
	s.imagePullMu.Unlock()
	go s.runImagePull(templateID, image, imageRuntime)
	return &copy
}

func (s *Server) runImagePull(templateID string, image runtime.Image, imageRuntime runtime.ImageRuntime) {
	err := imageRuntime.PullImage(context.Background(), image)
	s.imagePullMu.Lock()
	defer s.imagePullMu.Unlock()
	operation := s.imagePulls[templateID]
	if operation == nil {
		return
	}
	if err != nil {
		operation.Status = "failed"
		operation.Message = "Image pull failed. Check Podman availability and the image reference."
		return
	}
	available, err := imageRuntime.ImageAvailable(context.Background(), image)
	if err != nil || !available {
		operation.Status = "failed"
		operation.Message = "Image pull finished, but the image is not available locally."
		return
	}
	operation.Status = "available"
	operation.Message = "Available on this system."
}

func (s *Server) adminTemplateImagePull(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	templateValue, err := s.workspace.GetTemplate(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace template", http.StatusInternalServerError)
		return
	}
	imageRuntime, supported := s.runtime.(runtime.ImageRuntime)
	if !supported {
		http.Error(w, "image management is not supported by the runtime", http.StatusServiceUnavailable)
		return
	}
	operation := s.startImagePull(templateValue.ID, runtime.Image{Reference: templateValue.ImageReference, Digest: templateValue.ImageDigest}, imageRuntime)
	view := templateImageView{WorkspaceTemplate: templateValue, CSRFToken: s.ensureCSRF(w, r), Status: operation.Status, Message: operation.Message, Pulling: operation.Status == "pulling"}
	s.render(w, http.StatusOK, "admin-template-image-status", pageData{TemplateImage: &view})
}

func (s *Server) adminTemplateImageStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	templateValue, err := s.workspace.GetTemplate(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace template", http.StatusInternalServerError)
		return
	}
	views := s.templateImageViews(r.Context(), []domain.WorkspaceTemplate{templateValue}, s.ensureCSRF(w, r))
	s.render(w, http.StatusOK, "admin-template-image-status", pageData{TemplateImage: &views[0]})
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
		Groups:    s.templateGroups(r.Context(), user.ID),
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
		s.render(w, http.StatusBadRequest, "admin-template-form-page", pageData{Title: "Create template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form, Groups: s.templateGroups(r.Context(), user.ID)})
		return
	}
	if _, err := s.workspace.CreateTemplate(r.Context(), user.ID, input); err != nil {
		form.Error = templateFormError(err)
		s.render(w, http.StatusBadRequest, "admin-template-form-page", pageData{Title: "Create template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form, Groups: s.templateGroups(r.Context(), user.ID)})
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
	s.render(w, http.StatusOK, "admin-template-form-page", pageData{Title: "Edit template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: templateFormFromDomain(template), Groups: s.templateGroups(r.Context(), user.ID)})
}

func (s *Server) adminTemplatesCopy(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	templateValue, err := s.workspace.GetTemplate(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace template", http.StatusInternalServerError)
		return
	}
	form := templateFormFromDomain(templateValue)
	form.ID = ""
	form.Editing = false
	form.Name = ""
	s.render(w, http.StatusOK, "admin-template-form-page", pageData{Title: "Copy workspace template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form, Groups: s.templateGroups(r.Context(), user.ID)})
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
		s.render(w, http.StatusBadRequest, "admin-template-form-page", pageData{Title: "Edit template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form, Groups: s.templateGroups(r.Context(), user.ID)})
		return
	}
	if _, err := s.workspace.UpdateTemplate(r.Context(), user.ID, r.PathValue("id"), input); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		form.Error = templateFormError(err)
		s.render(w, status, "admin-template-form-page", pageData{Title: "Edit template | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Template: form, Groups: s.templateGroups(r.Context(), user.ID)})
		return
	}
	http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
}

func (s *Server) templateGroups(ctx context.Context, actorID string) []domain.Group {
	groups, err := s.auth.ListGroups(ctx, actorID)
	if err != nil {
		return nil
	}
	return groups
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
	templateID := r.PathValue("id")
	opErr := s.workspace.SetTemplateEnabled(r.Context(), user.ID, templateID, enabled)
	if isHTMXRequest(r) {
		s.renderTemplateRow(w, r, user, templateID, opErr)
		return
	}
	if opErr != nil {
		status := http.StatusBadRequest
		if errors.Is(opErr, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, templateActionError(opErr), status)
		return
	}
	http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
}

// renderTemplateRow is adminTemplateEnabled's dynamic-row counterpart to
// renderWorkspaceRow: it answers with just the affected <tr> so the
// templates list can update in place instead of reloading.
func (s *Server) renderTemplateRow(w http.ResponseWriter, r *http.Request, user domain.User, templateID string, opErr error) {
	if opErr != nil && errors.Is(opErr, repository.ErrNotFound) {
		w.WriteHeader(http.StatusOK)
		return
	}
	value, err := s.workspace.GetTemplate(r.Context(), user.ID, templateID)
	if errors.Is(err, repository.ErrNotFound) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		http.Error(w, "failed to load workspace template", http.StatusInternalServerError)
		return
	}
	view := s.templateImageViewFor(r.Context(), value, s.ensureCSRF(w, r))
	if opErr != nil {
		view.RowError = templateActionError(opErr)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "admin-template-row", view)
}

func (s *Server) adminRuntime(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	inspection, err := runtime.Inspect(r.Context(), s.runtime)
	status := http.StatusOK
	data := pageData{Title: "Runtime | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r)}
	if err != nil {
		status = http.StatusServiceUnavailable
		data.RuntimeError = "The configured container runtime is unavailable or returned invalid inspection data."
	} else {
		data.Inspection = &inspection
		csrfToken := data.CSRFToken
		views := s.runtimeWorkspaceViews(r.Context(), user.ID, inspection.Workspaces)
		for index := range views {
			views[index].CSRFToken = csrfToken
		}
		data.RuntimeWorkspaces = views
	}
	s.render(w, status, "admin-runtime-page", data)
}

// renderAdminRuntimeRow answers a dynamic row action on /admin/runtime with
// just the affected <tr>, mirroring the workspace list's row pattern. It
// re-runs the same live runtime inspection the full page uses (this page
// has no cheaper single-workspace path - unlike GetWorkspaceWithUsage, its
// rows are reconciled against Podman's own managed-container list, not just
// the stored record) and picks out the one matching row. A workspace no
// longer present in that reconciled view (successful delete, or a
// concurrent delete from another tab) renders as an empty response, which
// removes the row entirely.
func (s *Server) renderAdminRuntimeRow(w http.ResponseWriter, r *http.Request, user *domain.User, workspaceID, action string, opErr error) {
	rowError := ""
	if opErr != nil {
		rowError = workspaceActionError(action, opErr)
	}
	inspection, err := runtime.Inspect(r.Context(), s.runtime)
	if err != nil {
		http.Error(w, "the container runtime is unavailable", http.StatusServiceUnavailable)
		return
	}
	views := s.runtimeWorkspaceViews(r.Context(), user.ID, inspection.Workspaces)
	for _, view := range views {
		if view.WorkspaceID != workspaceID {
			continue
		}
		view.CSRFToken = s.ensureCSRF(w, r)
		view.RowError = rowError
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.templates.ExecuteTemplate(w, "admin-runtime-row", view)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	if s.options.Store == nil {
		http.Error(w, "audit storage is unavailable", http.StatusServiceUnavailable)
		return
	}
	eventType := strings.TrimSpace(r.URL.Query().Get("event"))
	events, err := s.options.Store.ListAuditEvents(r.Context(), domain.AuditQuery{Limit: 100, EventType: eventType})
	if err != nil {
		http.Error(w, "failed to load audit events", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "admin-audit-page", pageData{Title: "Audit | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), AuditEvents: events, AuditEventFilter: eventType})
}

func (s *Server) adminMetrics(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	view := &metricsView{ObservedAt: time.Now().UTC()}
	if capacity, err := runtimeCapacity(r.Context(), s.runtime); err != nil {
		view.CapacityError = "Host capacity is unavailable."
	} else {
		view.Capacity = capacity
	}
	if s.options.Store != nil {
		if allocated, err := s.options.Store.AllWorkspaceAllocations(r.Context()); err != nil {
			view.AllocationError = "Workspace allocation data is unavailable."
		} else {
			view.Allocated = allocated
		}
	}
	values, err := s.workspace.ListWorkspacesForRuntimeOverview(r.Context(), user.ID)
	if err == nil {
		view.Workspaces = make([]metricsWorkspaceView, 0, len(values))
		usageRuntime, _ := s.runtime.(runtime.ResourceUsageRuntime)
		for _, value := range values {
			item := metricsWorkspaceView{Workspace: value}
			if usageRuntime != nil && value.RuntimeID != "" && value.ObservedState == string(runtime.StateRunning) {
				if usage, usageErr := usageRuntime.WorkspaceResourceUsage(r.Context(), value.RuntimeID); usageErr == nil {
					item.Usage, item.Known = usage, true
				}
			}
			view.Workspaces = append(view.Workspaces, item)
		}
	}
	s.render(w, http.StatusOK, "admin-metrics-page", pageData{Title: "Metrics | COWS", User: &user, CSRFToken: s.ensureCSRF(w, r), Metrics: view})
}

func runtimeCapacity(ctx context.Context, adapter runtime.Runtime) (runtime.HostCapacity, error) {
	provider, ok := adapter.(runtime.CapacityProvider)
	if !ok {
		return runtime.HostCapacity{}, runtime.ErrNotSupported
	}
	return provider.HostCapacity(ctx)
}

func (s *Server) adminVolumes(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	if s.options.Store == nil {
		http.Error(w, "volume metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	values, err := s.options.Store.ListAllRetainedWorkspaceVolumes(r.Context())
	if err != nil {
		http.Error(w, "failed to load retained volumes", http.StatusInternalServerError)
		return
	}
	csrfToken := s.ensureCSRF(w, r)
	volumeRuntime, _ := s.runtime.(runtime.VolumeRuntime)
	views := make([]retainedVolumeView, 0, len(values))
	for _, value := range values {
		view := retainedVolumeView{RetainedWorkspaceVolume: value, CSRFToken: csrfToken}
		if volumeRuntime != nil {
			view.RuntimePresent, _ = volumeRuntime.VolumeExists(r.Context(), value.VolumeName)
		}
		views = append(views, view)
	}
	s.render(w, http.StatusOK, "admin-volumes-page", pageData{Title: "Retained volumes | COWS", User: &user, CSRFToken: csrfToken, RetainedVolumes: views})
}

func (s *Server) retainedVolumeForRoute(ctx context.Context, workspaceID, mountName string) (domain.RetainedWorkspaceVolume, error) {
	if s.options.Store == nil || workspaceID == "" || mountName == "" {
		return domain.RetainedWorkspaceVolume{}, repository.ErrNotFound
	}
	values, err := s.options.Store.ListRetainedWorkspaceVolumes(ctx, workspaceID)
	if err != nil {
		return domain.RetainedWorkspaceVolume{}, err
	}
	for _, value := range values {
		if value.MountName == mountName {
			return value, nil
		}
	}
	return domain.RetainedWorkspaceVolume{}, repository.ErrNotFound
}

func (s *Server) adminVolumeDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	volume, err := s.retainedVolumeForRoute(r.Context(), r.PathValue("workspace_id"), r.PathValue("mount_name"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained volume", http.StatusInternalServerError)
		return
	}
	reader, err := s.files.OpenRuntimeZip(r.Context(), runtime.FileAccessSpec{MountType: "volume", Source: volume.VolumeName, ContainerPath: volume.ContainerPath, ContainerUID: 0, ContainerGID: 0, ReadOnly: true}, volume.MountName)
	if err != nil {
		http.Error(w, "the retained volume could not be read", http.StatusServiceUnavailable)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+volume.MountName+`.zip"`)
	s.recordAdminAudit(r.Context(), user.ID, "volume.recovery_downloaded", volume.VolumeName, map[string]string{"workspace_id": volume.WorkspaceID, "mount_name": volume.MountName})
	_, _ = io.Copy(w, reader)
}

func (s *Server) adminVolumeDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	volume, err := s.retainedVolumeForRoute(r.Context(), r.PathValue("workspace_id"), r.PathValue("mount_name"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained volume", http.StatusInternalServerError)
		return
	}
	volumeRuntime, ok := s.runtime.(runtime.VolumeRuntime)
	rowErrorText := ""
	if !ok {
		rowErrorText = "Named volume management is unavailable."
	} else if err := volumeRuntime.RemoveVolume(r.Context(), volume.VolumeName); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		rowErrorText = "The retained volume could not be removed."
	} else if err := s.options.Store.DeleteRetainedWorkspaceVolume(r.Context(), volume.VolumeName); err != nil && !errors.Is(err, repository.ErrNotFound) {
		rowErrorText = "The retained volume metadata could not be removed."
	}
	if rowErrorText == "" {
		s.recordAdminAudit(r.Context(), user.ID, "volume.recovery_removed", volume.VolumeName, map[string]string{"workspace_id": volume.WorkspaceID, "mount_name": volume.MountName})
	}
	if isHTMXRequest(r) {
		s.renderAdminVolumeRow(w, r, volume.WorkspaceID, volume.MountName, rowErrorText)
		return
	}
	if rowErrorText != "" {
		http.Error(w, rowErrorText, http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/admin/volumes", http.StatusSeeOther)
}

// renderAdminVolumeRow answers a row-action request with just the affected
// <tr>. An empty response (tombstone gone) removes the row.
func (s *Server) renderAdminVolumeRow(w http.ResponseWriter, r *http.Request, workspaceID, mountName, rowError string) {
	volume, err := s.retainedVolumeForRoute(r.Context(), workspaceID, mountName)
	if errors.Is(err, repository.ErrNotFound) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained volume", http.StatusInternalServerError)
		return
	}
	view := retainedVolumeView{RetainedWorkspaceVolume: volume, RowError: rowError, CSRFToken: s.ensureCSRF(w, r)}
	if volumeRuntime, ok := s.runtime.(runtime.VolumeRuntime); ok {
		view.RuntimePresent, _ = volumeRuntime.VolumeExists(r.Context(), volume.VolumeName)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "admin-volume-row", view)
}

func (s *Server) recordAdminAudit(ctx context.Context, actorID, eventType, targetID string, metadata map[string]string) {
	if s.options.Store != nil {
		_ = s.options.Store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: eventType, TargetType: "volume", TargetID: targetID, Metadata: metadata, CreatedAt: time.Now().UTC()})
	}
}

func (s *Server) adminDirectories(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	if s.options.Store == nil {
		http.Error(w, "directory metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	values, err := s.options.Store.ListAllRetainedWorkspaceDirectories(r.Context())
	if err != nil {
		http.Error(w, "failed to load retained directories", http.StatusInternalServerError)
		return
	}
	csrfToken := s.ensureCSRF(w, r)
	views := make([]retainedDirectoryView, 0, len(values))
	for _, value := range values {
		views = append(views, retainedDirectoryView{RetainedWorkspaceDirectory: value, CSRFToken: csrfToken})
	}
	s.render(w, http.StatusOK, "admin-directories-page", pageData{Title: "Retained directories | COWS", User: &user, CSRFToken: csrfToken, RetainedDirectories: views})
}

func (s *Server) adminDirectoryDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	if s.options.Store == nil {
		http.Error(w, "directory metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	workspaceID := r.PathValue("workspace_id")
	directory, err := s.options.Store.FindRetainedWorkspaceDirectoryByID(r.Context(), workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained directory", http.StatusInternalServerError)
		return
	}
	reader, name, err := s.workspace.OpenRetainedDirectoryZipByID(r.Context(), workspaceID)
	if err != nil {
		http.Error(w, "the retained directory could not be read", http.StatusServiceUnavailable)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
	s.recordStorageAudit(r.Context(), user.ID, "directory.recovery_downloaded", "directory", directory.WorkspaceID, map[string]string{"workspace_id": directory.WorkspaceID, "owner_user_id": directory.OwnerUserID})
	_, _ = io.Copy(w, reader)
}

func (s *Server) adminDirectoryDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if s.options.Store == nil {
		http.Error(w, "directory metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	workspaceID := r.PathValue("workspace_id")
	directory, err := s.options.Store.FindRetainedWorkspaceDirectoryByID(r.Context(), workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained directory", http.StatusInternalServerError)
		return
	}
	rowErrorText := ""
	if err := s.workspace.DeleteRetainedDirectoryByID(r.Context(), workspaceID); err != nil {
		rowErrorText = "The retained directory could not be removed."
	} else {
		s.recordStorageAudit(r.Context(), user.ID, "directory.recovery_removed", "directory", directory.WorkspaceID, map[string]string{"workspace_id": directory.WorkspaceID, "owner_user_id": directory.OwnerUserID})
	}
	if isHTMXRequest(r) {
		if rowErrorText == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		view := retainedDirectoryView{RetainedWorkspaceDirectory: directory, RowError: rowErrorText, CSRFToken: s.ensureCSRF(w, r)}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.templates.ExecuteTemplate(w, "admin-directory-row", view)
		return
	}
	if rowErrorText != "" {
		http.Error(w, rowErrorText, http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/admin/directories", http.StatusSeeOther)
}

func (s *Server) runtimeWorkspaceViews(ctx context.Context, actorID string, observations []runtime.ObservedWorkspace) []runtimeWorkspaceView {
	values, err := s.workspace.ListWorkspacesForRuntimeOverview(ctx, actorID)
	if err != nil {
		return nil
	}
	users, err := s.auth.ListUsers(ctx, actorID)
	if err != nil {
		return nil
	}
	templates, err := s.workspace.ListTemplates(ctx, actorID)
	if err != nil {
		return nil
	}
	workspaceByID := make(map[string]domain.Workspace, len(values))
	for _, value := range values {
		workspaceByID[value.ID] = value
	}
	userByID := make(map[string]domain.User, len(users))
	for _, value := range users {
		userByID[value.ID] = value
	}
	templateByID := make(map[string]domain.WorkspaceTemplate, len(templates))
	for _, value := range templates {
		templateByID[value.ID] = value
	}
	observedByWorkspace := make(map[string]runtime.ObservedWorkspace, len(observations))
	for _, observed := range observations {
		observedByWorkspace[observed.WorkspaceID] = observed
	}
	views := make([]runtimeWorkspaceView, 0, len(values)+len(observations))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		observed, present := observedByWorkspace[value.ID]
		if !present {
			observed = runtime.ObservedWorkspace{RuntimeID: value.RuntimeID, WorkspaceID: value.ID, State: runtime.State(value.ObservedState), ObservedAt: value.ObservedAt}
			if value.RuntimeID != "" {
				// A stale running record must not hide the fact that Podman no
				// longer reports the container. Start will recreate it safely.
				observed.State = runtime.StateUnknown
				if value.ObservedState != string(runtime.StateRemoved) {
					observed.State = runtime.State("missing")
				}
			}
		}
		view := runtimeWorkspaceView{Observed: observed, WorkspaceID: value.ID, Managed: true, RuntimePresent: present}
		view.OwnerID = value.OwnerUserID
		if owner, exists := userByID[value.OwnerUserID]; exists {
			view.Owner = owner.Username
		}
		if template, exists := templateByID[value.TemplateID]; exists {
			view.Template = template.Name
		}
		if template, exists := templateByID[value.TemplateID]; exists {
			for _, method := range template.AccessMethods {
				switch method {
				case domain.AccessTerminal:
					view.Access.Terminal = true
				case domain.AccessDesktop:
					view.Access.Desktop = true
				case domain.AccessFiles:
					view.Access.Files = true
				case domain.AccessLogs:
					view.Access.Logs = true
				}
			}
		}
		views = append(views, view)
		seen[value.ID] = struct{}{}
	}
	for _, observed := range observations {
		if _, exists := seen[observed.WorkspaceID]; exists {
			continue
		}
		views = append(views, runtimeWorkspaceView{Observed: observed, WorkspaceID: observed.WorkspaceID})
	}
	return views
}

func (s *Server) parseTemplateForm(r *http.Request) (templateFormData, workspace.TemplateInput, error) {
	form := templateFormData{
		Name:                   r.FormValue("name"),
		Description:            r.FormValue("description"),
		ImageReference:         r.FormValue("image_reference"),
		ImageDigest:            r.FormValue("image_digest"),
		DefaultCPUMillis:       r.FormValue("default_cpu_millis"),
		MaxCPUMillis:           r.FormValue("max_cpu_millis"),
		DefaultMemoryMiB:       r.FormValue("default_memory_mib"),
		MaxMemoryMiB:           r.FormValue("max_memory_mib"),
		ResourcesConfigurable:  r.FormValue("resources_configurable") == "on",
		InitialConnectionHours: r.FormValue("initial_connection_hours"),
		StoppedRetentionHours:  r.FormValue("stopped_retention_hours"),
		ConfigurationJSON:      r.FormValue("configuration_json"),
		Enabled:                r.FormValue("enabled") == "on",
		GroupAccessMode:        r.FormValue("group_access_mode"),
		AllowedGroupIDs:        append([]string(nil), r.Form["allowed_group_ids"]...),
	}
	for _, method := range r.Form["access_methods"] {
		switch domain.AccessMethod(method) {
		case domain.AccessTerminal:
			form.TerminalAccess = true
		case domain.AccessDesktop:
			form.DesktopAccess = true
		case domain.AccessFiles:
			form.FilesAccess = true
		case domain.AccessLogs:
			form.LogsAccess = true
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
	initialConnection, err := parseDurationInput(form.InitialConnectionHours, time.Hour)
	if err != nil {
		return form, workspace.TemplateInput{}, workspace.ErrInvalidTemplate
	}
	stoppedRetention, err := parseDurationInput(form.StoppedRetentionHours, time.Hour)
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
	configuration := domain.TemplateConfiguration{}
	if strings.TrimSpace(form.ConfigurationJSON) != "" {
		if err := decodeTemplateConfiguration(form.ConfigurationJSON, &configuration); err != nil {
			return form, workspace.TemplateInput{}, workspace.ErrInvalidTemplate
		}
	}
	form.TerminalRootAllowed = hasTerminalUID(configuration, 0)
	methods := make([]domain.AccessMethod, 0, 4)
	if form.TerminalAccess {
		methods = append(methods, domain.AccessTerminal)
	}
	if form.DesktopAccess {
		methods = append(methods, domain.AccessDesktop)
	}
	if form.FilesAccess {
		methods = append(methods, domain.AccessFiles)
	}
	if form.LogsAccess {
		methods = append(methods, domain.AccessLogs)
	}
	roles := make([]domain.Role, 0, 2)
	if form.UserRole {
		roles = append(roles, domain.RoleUser)
	}
	if form.AdministratorRole {
		roles = append(roles, domain.RoleAdministrator)
	}
	return form, workspace.TemplateInput{
		Name:                            form.Name,
		Description:                     form.Description,
		ImageReference:                  form.ImageReference,
		ImageDigest:                     form.ImageDigest,
		DefaultCPUMillis:                defaultCPU,
		MaxCPUMillis:                    maxCPU,
		DefaultMemoryBytes:              defaultMemory,
		MaxMemoryBytes:                  maxMemory,
		ResourcesConfigurable:           form.ResourcesConfigurable,
		Configuration:                   configuration,
		InitialConnectionTimeoutSeconds: initialConnection,
		StoppedRetentionSeconds:         stoppedRetention,
		AccessMethods:                   methods,
		AllowedRoles:                    roles,
		GroupAccessMode:                 form.GroupAccessMode,
		AllowedGroupIDs:                 form.AllowedGroupIDs,
		Enabled:                         form.Enabled,
	}, nil
}

func decodeTemplateConfiguration(value string, configuration *domain.TemplateConfiguration) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(configuration); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("template configuration has trailing JSON")
		}
		return err
	}
	return nil
}

func parsePositiveInt(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("value must be positive")
	}
	return parsed, nil
}

func parseOptionalPositiveInt(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return parsePositiveInt(value)
}

func parseResource(value string, multiplier int64) (int64, error) {
	parsed, err := parsePositiveInt(value)
	if err != nil || parsed > (1<<62)/multiplier {
		return 0, errors.New("resource value is invalid")
	}
	return parsed * multiplier, nil
}

func parseDurationInput(value string, unit time.Duration) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	parsed, err := parseNonNegativeInt(value)
	if err != nil || parsed > int64((10*365*24*time.Hour)/unit) {
		return 0, errors.New("duration value is invalid")
	}
	return parsed * int64(unit/time.Second), nil
}

func formatTimeout(seconds int64) string {
	if seconds <= 0 {
		return "disabled"
	}
	days := seconds / (24 * 60 * 60)
	seconds %= 24 * 60 * 60
	hours := seconds / (60 * 60)
	seconds %= 60 * 60
	minutes := seconds / 60
	parts := make([]string, 0, 3)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, " ")
}

func formatFileSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", size)
}

func timeoutPhaseText(phase workspace.TimeoutPhase) string {
	switch phase {
	case workspace.TimeoutPhaseAwaitingConnection:
		return "Awaiting first connection"
	case workspace.TimeoutPhaseStoppedRetention:
		return "Stopped container retention"
	default:
		return "No active timeout"
	}
}

// timeoutCountdownText renders a live "stop in 2d 21h" / "delete in 3d 2h"
// readout. The verb comes from Phase, not Action: Action only flips away
// from "none" once the deadline has actually passed (see
// workspace.EvaluateTimeouts), but Phase already tells us which action a
// still-pending deadline is heading toward.
func timeoutCountdownText(status workspace.TimeoutStatus, now time.Time) string {
	var verb string
	switch status.Phase {
	case workspace.TimeoutPhaseAwaitingConnection:
		verb = "stop"
	case workspace.TimeoutPhaseStoppedRetention:
		verb = "delete"
	default:
		return ""
	}
	if status.Deadline.IsZero() {
		return ""
	}
	remaining := status.Deadline.Sub(now)
	if remaining <= 0 {
		return verb + " due now"
	}
	return verb + " in " + formatTimeout(int64(remaining.Seconds()))
}

// timeoutUrgencyClass escalates the countdown's color as its deadline
// nears, so a user glancing at the list notices an imminent auto-stop or
// auto-delete without having to read the exact duration.
func timeoutUrgencyClass(status workspace.TimeoutStatus, now time.Time) string {
	if status.Deadline.IsZero() {
		return ""
	}
	switch remaining := status.Deadline.Sub(now); {
	case remaining <= 6*time.Hour:
		return "timeout-urgent"
	case remaining <= 48*time.Hour:
		return "timeout-soon"
	default:
		return ""
	}
}

// workspaceInitials derives a two-letter badge label from a workspace name,
// e.g. "iic-osic-tools-dev" -> "IO". Purely cosmetic; any deterministic
// rule is fine since it only has to be stable and short.
func workspaceInitials(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == ' ' || r == '.'
	})
	switch len(words) {
	case 0:
		return "?"
	case 1:
		runes := []rune(words[0])
		if len(runes) >= 2 {
			return strings.ToUpper(string(runes[:2]))
		}
		return strings.ToUpper(string(runes))
	default:
		first := []rune(words[0])[:1]
		second := []rune(words[1])[:1]
		return strings.ToUpper(string(first) + string(second))
	}
}

// templateHue derives a stable badge hue (0-359) from a template ID, so the
// same template always gets the same badge color across every row and page
// it appears on, without an administrator having to assign one.
func templateHue(templateID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(templateID))
	return int(h.Sum32() % 360)
}

// workspaceStatusBucket collapses the runtime's finer-grained observed
// states into the three buckets the Overview's filter pills offer. Shared
// by the filter counts and each row's data-status attribute so the two
// never drift apart.
func workspaceStatusBucket(observedState string) string {
	switch observedState {
	case "running":
		return "running"
	case "failed":
		return "error"
	default:
		return "stopped"
	}
}

// statusDotView is the pill-and-dot status treatment (workspaces table,
// detail header): a small colored dot next to a label, pulsing while a
// lifecycle operation is actively in progress. This is deliberately
// separate from the app's other signature status component, the bracketed
// `.status-stamp` (`[ RUNNING ]`, used by admin/runtime and everywhere
// else state is shown, see ARCHITECTURE.md) - these two pages adopt the
// mockup's dot style specifically, the rest of the app keeps the bracket
// convention.
type statusDotView struct {
	Label   string
	Bucket  string
	Pulsing bool
}

func workspaceStatusDot(value domain.Workspace) statusDotView {
	if value.OperationStatus == "running" {
		return statusDotView{Label: operationText(value.Operation) + "…", Bucket: "warn", Pulsing: true}
	}
	return stateStatusDot(value.ObservedState)
}

// stateStatusDot buckets a raw runtime/observed state string into the
// status-dot component's color scheme, without the workspace-record-only
// concept of an in-progress operation (workspaceStatusDot layers that on
// top for pages backed by a stored workspace; admin/runtime and metrics
// show live runtime.ObservedWorkspace state directly, including synthetic
// values like "missing" that aren't in domain.Workspace's own vocabulary,
// so they call this directly).
func stateStatusDot(state string) statusDotView {
	capitalized := state
	if capitalized != "" {
		capitalized = strings.ToUpper(capitalized[:1]) + capitalized[1:]
	}
	switch state {
	case "running":
		return statusDotView{Label: "Running", Bucket: "ok"}
	case "created", "stopped":
		return statusDotView{Label: capitalized, Bucket: "neutral"}
	case "exited", "removed", "failed":
		return statusDotView{Label: capitalized, Bucket: "bad"}
	default:
		return statusDotView{Label: capitalized, Bucket: "warn"}
	}
}

// booleanStatusDot renders the status-dot component for simple two-state
// concepts (enabled/disabled, present/missing, active/disabled) that don't
// carry workspace lifecycle's richer semantics.
func booleanStatusDot(on bool, onLabel, offLabel string) statusDotView {
	if on {
		return statusDotView{Label: onLabel, Bucket: "ok"}
	}
	return statusDotView{Label: offLabel, Bucket: "bad"}
}

func workspaceStatusCounts(workspaces []domain.Workspace) map[string]int {
	counts := map[string]int{"all": len(workspaces), "running": 0, "stopped": 0, "error": 0}
	for _, value := range workspaces {
		counts[workspaceStatusBucket(value.ObservedState)]++
	}
	return counts
}

// relativeTime renders a short "3 min ago" / "4 days ago" readout for
// timestamps like LastConnectedAt, where the exact instant matters less
// than roughly how long ago it was.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	d := time.Since(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < 90*time.Second:
		return "1 min ago"
	case d < 60*time.Minute:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 90*time.Minute:
		return "1 hour ago"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "1 day ago"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

func operationText(operation string) string {
	switch operation {
	case "start":
		return "Start"
	case "stop":
		return "Stop"
	case "delete":
		return "Delete"
	case "timeout-stop":
		return "Automatic stop"
	case "timeout-delete":
		return "Automatic deletion"
	case "start:preparing":
		return "Preparing"
	case "start:creating":
		return "Creating container"
	case "start:starting":
		return "Starting container"
	case "stop:stopping":
		return "Stopping container"
	case "delete:removing":
		return "Removing container"
	case "delete:archiving":
		return "Archiving workspace data"
	default:
		return "Workspace operation"
	}
}

func observedErrorText(code string) string {
	switch code {
	case "runtime_create_failed":
		return "Container creation failed."
	case "runtime_start_failed":
		return "Container start failed."
	case "runtime_missing":
		return "The managed container is missing from Podman."
	default:
		return "The runtime reported a workspace problem."
	}
}

func parseQuotaForm(form quotaFormData) (quota.Input, error) {
	cpu, err := parseNonNegativeInt(form.MaxCPUMillis)
	if err != nil {
		return quota.Input{}, quota.ErrInvalidQuota
	}
	memory, err := parseNonNegativeResource(form.MaxMemoryMiB, 1<<20)
	if err != nil {
		return quota.Input{}, quota.ErrInvalidQuota
	}
	storage, err := parseNonNegativeResource(form.MaxStorageGiB, 1<<30)
	if err != nil {
		return quota.Input{}, quota.ErrInvalidQuota
	}
	workspaces, err := parseNonNegativeInt(form.MaxWorkspaces)
	if err != nil {
		return quota.Input{}, quota.ErrInvalidQuota
	}
	runningWorkspaces, err := parseNonNegativeInt(form.MaxRunningWorkspaces)
	if err != nil {
		return quota.Input{}, quota.ErrInvalidQuota
	}
	return quota.Input{MaxCPUMillis: cpu, MaxMemoryBytes: memory, MaxStorageBytes: storage, MaxWorkspaces: workspaces, MaxRunningWorkspaces: runningWorkspaces}, nil
}

func quotaFormFromUserQuota(value domain.UserQuota) quotaFormData {
	return quotaFormData{
		UserID:               value.UserID,
		MaxCPUMillis:         strconv.FormatInt(value.MaxCPUMillis, 10),
		MaxMemoryMiB:         strconv.FormatInt(value.MaxMemoryBytes/(1<<20), 10),
		MaxStorageGiB:        strconv.FormatInt(value.MaxStorageBytes/(1<<30), 10),
		MaxWorkspaces:        strconv.FormatInt(value.MaxWorkspaces, 10),
		MaxRunningWorkspaces: strconv.FormatInt(value.MaxRunningWorkspaces, 10),
	}
}

func quotaFormFromGroupQuota(value domain.GroupQuota) quotaFormData {
	return quotaFormData{
		GroupID:              value.GroupID,
		MaxCPUMillis:         strconv.FormatInt(value.MaxCPUMillis, 10),
		MaxMemoryMiB:         strconv.FormatInt(value.MaxMemoryBytes/(1<<20), 10),
		MaxStorageGiB:        strconv.FormatInt(value.MaxStorageBytes/(1<<30), 10),
		MaxWorkspaces:        strconv.FormatInt(value.MaxWorkspaces, 10),
		MaxRunningWorkspaces: strconv.FormatInt(value.MaxRunningWorkspaces, 10),
	}
}

func parseSettingsForm(form settingsFormData) (quota.HostSettingsInput, error) {
	storage, err := parseNonNegativeResource(form.HostStorageGiB, 1<<30)
	if err != nil {
		return quota.HostSettingsInput{}, quota.ErrInvalidQuota
	}
	cpuFactor, err := strconv.ParseFloat(strings.TrimSpace(form.CPUOverbookingFactor), 64)
	if err != nil || math.IsNaN(cpuFactor) || math.IsInf(cpuFactor, 0) || cpuFactor < quota.MinOverbookingFactor || cpuFactor > quota.MaxOverbookingFactor {
		return quota.HostSettingsInput{}, quota.ErrInvalidQuota
	}
	memoryFactor, err := strconv.ParseFloat(strings.TrimSpace(form.MemoryOverbookingFactor), 64)
	if err != nil || math.IsNaN(memoryFactor) || math.IsInf(memoryFactor, 0) || memoryFactor < quota.MinOverbookingFactor || memoryFactor > quota.MaxOverbookingFactor {
		return quota.HostSettingsInput{}, quota.ErrInvalidQuota
	}
	reservedStorage, err := parseNonNegativeResource(form.ReservedStorageGiB, 1<<30)
	if err != nil {
		return quota.HostSettingsInput{}, quota.ErrInvalidQuota
	}
	return quota.HostSettingsInput{HostStorageBytes: storage, CPUOverbookingFactor: cpuFactor, MemoryOverbookingFactor: memoryFactor, ReservedStorageBytes: reservedStorage}, nil
}

func parseNonNegativeInt(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("value must be zero or positive")
	}
	return parsed, nil
}

func parseNonNegativeResource(value string, multiplier int64) (int64, error) {
	parsed, err := parseNonNegativeInt(value)
	if err != nil || parsed > (1<<62)/multiplier {
		return 0, errors.New("resource value is invalid")
	}
	return parsed * multiplier, nil
}

func settingsFormFromDomain(settings domain.HostSettings) settingsFormData {
	return settingsFormData{
		HostStorageGiB:          strconv.FormatInt(settings.HostStorageBytes/(1<<30), 10),
		CPUOverbookingFactor:    strconv.FormatFloat(settings.CPUOverbookingFactor, 'f', -1, 64),
		MemoryOverbookingFactor: strconv.FormatFloat(settings.MemoryOverbookingFactor, 'f', -1, 64),
		ReservedStorageGiB:      strconv.FormatInt(settings.ReservedStorageBytes/(1<<30), 10),
	}
}

func defaultTemplateForm() templateFormData {
	return templateFormData{DefaultCPUMillis: "1000", MaxCPUMillis: "4000", DefaultMemoryMiB: "2048", MaxMemoryMiB: "8192", InitialConnectionHours: "24", StoppedRetentionHours: "24", ConfigurationJSON: "{}", TerminalAccess: true, UserRole: true, GroupAccessMode: domain.GroupAccessExclude, Enabled: true}
}

func hasTerminalUID(configuration domain.TemplateConfiguration, want int64) bool {
	for _, uid := range configuration.TerminalUIDs {
		if uid == want {
			return true
		}
	}
	return false
}

func templateFormFromDomain(template domain.WorkspaceTemplate) templateFormData {
	configurationJSON, _ := json.MarshalIndent(template.Configuration, "", "  ")
	if len(configurationJSON) == 0 {
		configurationJSON = []byte("{}")
	}
	form := templateFormData{ID: template.ID, Editing: true, Name: template.Name, Description: template.Description, ImageReference: template.ImageReference, ImageDigest: template.ImageDigest, DefaultCPUMillis: strconv.FormatInt(template.DefaultCPUMillis, 10), MaxCPUMillis: strconv.FormatInt(template.MaxCPUMillis, 10), DefaultMemoryMiB: strconv.FormatInt(template.DefaultMemoryBytes/(1<<20), 10), MaxMemoryMiB: strconv.FormatInt(template.MaxMemoryBytes/(1<<20), 10), ResourcesConfigurable: template.ResourcesConfigurable, InitialConnectionHours: strconv.FormatInt(template.InitialConnectionTimeoutSeconds/3600, 10), StoppedRetentionHours: strconv.FormatInt(template.StoppedRetentionSeconds/3600, 10), GroupAccessMode: template.GroupAccessMode, AllowedGroupIDs: append([]string(nil), template.AllowedGroupIDs...), ConfigurationJSON: string(configurationJSON), TerminalRootAllowed: hasTerminalUID(template.Configuration, 0), Enabled: template.Enabled}
	for _, method := range template.AccessMethods {
		switch method {
		case domain.AccessTerminal:
			form.TerminalAccess = true
		case domain.AccessDesktop:
			form.DesktopAccess = true
		case domain.AccessFiles:
			form.FilesAccess = true
		case domain.AccessLogs:
			form.LogsAccess = true
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
	if user.MustChangePassword {
		http.Redirect(w, r, "/account/password", http.StatusSeeOther)
		return domain.User{}, false
	}
	if !user.IsAdministrator() {
		http.Error(w, "administrator permission required", http.StatusForbidden)
		return domain.User{}, false
	}
	return *user, true
}

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (*domain.User, bool) {
	user, err := s.currentUser(r)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return nil, false
	}
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, false
	}
	if user.MustChangePassword {
		http.Redirect(w, r, "/account/password", http.StatusSeeOther)
		return nil, false
	}
	if user.Disabled {
		http.Error(w, "account disabled", http.StatusForbidden)
		return nil, false
	}
	return user, true
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
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", MaxAge: int(s.options.SessionLifetime.Seconds()), Secure: s.options.CookieSecure, SameSite: http.SameSiteLaxMode})
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
		return "Use a valid username, email address, display name, supported role, and password between 12 and 72 characters."
	case errors.Is(err, repository.ErrConflict):
		return "A user with that username already exists."
	default:
		return "The user could not be created."
	}
}

func registrationFormError(err error) string {
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		return "Use a valid username, email address, display name, and matching password of 12 to 72 characters."
	case errors.Is(err, repository.ErrConflict):
		return "Registration could not be completed with those details."
	case errors.Is(err, auth.ErrRegistrationUnavailable):
		return "Registration is temporarily unavailable. Try again later."
	default:
		return "Registration could not be completed."
	}
}

func userActionError(err error) string {
	switch {
	case errors.Is(err, auth.ErrSelfDisable):
		return "You cannot disable your own administrator account."
	case errors.Is(err, auth.ErrLastAdministrator):
		return "The last active administrator cannot be disabled."
	case errors.Is(err, workspace.ErrWorkspaceCleanupIncomplete):
		return "The user could not be fully cleaned up. Check Runtime and retry."
	default:
		return "The user state could not be changed."
	}
}

func userDeleteError(err error) string {
	switch {
	case errors.Is(err, auth.ErrSelfDelete):
		return "You cannot delete your own administrator account."
	case errors.Is(err, auth.ErrUserMustBeDisabled), errors.Is(err, workspace.ErrUserCleanupRequiresDisabled):
		return "The user must be disabled before deletion."
	case errors.Is(err, workspace.ErrWorkspaceCleanupIncomplete):
		return "One or more workspaces could not be deleted. The user remains disabled; check Runtime and retry."
	case errors.Is(err, workspace.ErrRuntimeUnavailable):
		return "The rootless Podman runtime is unavailable. User deletion is paused until it is available."
	default:
		return "The user could not be deleted."
	}
}

func groupActionError(err error) string {
	switch {
	case errors.Is(err, auth.ErrGroupInUse):
		return "This group is still referenced by a workspace template. Remove it from those templates first."
	default:
		return "The group could not be deleted."
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

func fileErrorText(err error) string {
	switch {
	case errors.Is(err, workspace.ErrFileManagerNotAvailable), errors.Is(err, files.ErrFileManagerAccess):
		return "File manager access is not enabled for this workspace."
	case errors.Is(err, files.ErrReadOnly):
		return "This mount is read-only."
	case errors.Is(err, files.ErrNotFound):
		return "The requested file or directory was not found."
	case errors.Is(err, files.ErrNameConflict):
		return "A file or directory with that name already exists."
	case errors.Is(err, files.ErrUploadTooLarge):
		return "The upload exceeds the 128 MiB limit."
	case errors.Is(err, files.ErrInvalidPath):
		return "The requested path or filename is invalid."
	case errors.Is(err, files.ErrNotDirectory):
		return "Only directories can be downloaded as ZIP archives."
	case errors.Is(err, files.ErrArchiveTooLarge):
		return "The directory is too large to download as a ZIP archive."
	case errors.Is(err, files.ErrArchiveTooMany):
		return "The directory contains too many entries to download as a ZIP archive."
	case errors.Is(err, files.ErrTooManyEntries):
		return "This directory contains too many entries to manage through COWS."
	case errors.Is(err, files.ErrDownloadTooLarge):
		return "The requested download exceeds the 4 GiB limit."
	default:
		return "The file operation could not be completed."
	}
}

func fileErrorStatus(err error) int {
	switch {
	case errors.Is(err, workspace.ErrFileManagerNotAvailable), errors.Is(err, files.ErrFileManagerAccess):
		return http.StatusForbidden
	case errors.Is(err, files.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, files.ErrNameConflict):
		return http.StatusConflict
	case errors.Is(err, files.ErrTooManyEntries), errors.Is(err, files.ErrDownloadTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusBadRequest
	}
}

func templateActionError(err error) string {
	if errors.Is(err, repository.ErrNotFound) {
		return "The workspace template was not found."
	}
	return "The workspace template state could not be changed."
}

func workspaceFormError(err error) string {
	switch {
	case errors.Is(err, workspace.ErrInvalidWorkspaceResources):
		return "The selected resources must stay between the template defaults and maximums."
	case errors.Is(err, workspace.ErrInvalidWorkspace):
		return "Enter a workspace name and select an approved template."
	case errors.Is(err, workspace.ErrTemplateNotAvailable):
		return "That workspace template is not available for your account."
	case errors.Is(err, repository.ErrConflict):
		return "You already have a workspace with that name."
	case errors.Is(err, quota.ErrQuotaUnavailable):
		return "An administrator must assign your resource quota before creating a workspace."
	case errors.Is(err, quota.ErrQuotaExceeded):
		return "This workspace would exceed your assigned quota."
	case errors.Is(err, quota.ErrCapacityUnavailable):
		return "Host capacity is currently unavailable; try again later."
	case errors.Is(err, quota.ErrCapacityInsufficient):
		var capacityErr *quota.CapacityInsufficientError
		if errors.As(err, &capacityErr) {
			return fmt.Sprintf("The host does not have enough remaining %s capacity for this workspace.", capacityErr.Resource)
		}
		return "The host does not have enough remaining capacity for this workspace."
	default:
		return "The workspace could not be created."
	}
}

func workspaceActionError(action string, err error) string {
	verb := workspaceActionVerb(action)
	var quotaErr *quota.QuotaInsufficientError
	var capacityErr *quota.CapacityInsufficientError
	switch {
	case errors.Is(err, quota.ErrQuotaUnavailable):
		return "This workspace cannot be " + verb + " because your resource quota is not assigned."
	case errors.As(err, &quotaErr):
		if quotaErr.Resource == "running workspaces" {
			return fmt.Sprintf("This workspace cannot be %s because your running-workspace quota is full (%d of %d).", verb, quotaErr.Current, quotaErr.Limit)
		}
		return fmt.Sprintf("This workspace cannot be %s because it would exceed your assigned %s quota (%s allocated, %s requested, %s limit).", verb, quotaErr.Resource, resourceAmount(quotaErr.Resource, quotaErr.Current), resourceAmount(quotaErr.Resource, quotaErr.Requested), resourceAmount(quotaErr.Resource, quotaErr.Limit))
	case errors.As(err, &capacityErr):
		return fmt.Sprintf("This workspace cannot be %s because the host has only %s available, but it needs %s.", verb, resourceAmount(capacityErr.Resource, capacityErr.Available), resourceAmount(capacityErr.Resource, capacityErr.Requested))
	case errors.Is(err, quota.ErrCapacityUnavailable):
		return "This workspace cannot be " + verb + " because current host capacity is unavailable."
	case errors.Is(err, workspace.ErrRuntimeUnavailable):
		return "The rootless Podman runtime is unavailable. Try again later."
	case errors.Is(err, runtime.ErrUnavailable):
		return "This workspace could not be " + verb + " because the rootless Podman runtime is unavailable."
	case errors.Is(err, runtime.ErrConflict):
		return "This workspace could not be " + verb + " because the runtime reported a conflicting container state. Reconcile the workspace and try again."
	case errors.Is(err, runtime.ErrNotFound):
		return "This workspace could not be " + verb + " because its container no longer exists. Reconcile the workspace and try again."
	case errors.Is(err, context.DeadlineExceeded):
		return "This workspace could not be " + verb + " before the operation timed out."
	case errors.Is(err, workspace.ErrWorkspaceStateConflict):
		return "This workspace is not in a state where it can be " + verb + "."
	case errors.Is(err, runtime.ErrNotSupported):
		return "This operation is not supported by the configured rootless Podman runtime."
	case errors.Is(err, quota.ErrQuotaExceeded):
		return "This workspace cannot be " + verb + " because it would exceed your assigned resource quota."
	default:
		return "The workspace could not be " + verb + "."
	}
}

func resourceAmount(resource string, value int64) string {
	if strings.EqualFold(resource, "memory") {
		return fmt.Sprintf("%d MiB", value/(1<<20))
	}
	return fmt.Sprintf("%d mCPU", value)
}

func terminalErrorText(err error) string {
	switch {
	case errors.Is(err, workspace.ErrRuntimeUnavailable), errors.Is(err, runtime.ErrUnavailable):
		return "The container runtime is unavailable."
	case errors.Is(err, workspace.ErrWorkspaceStateConflict):
		return "The workspace must be running before a terminal can be opened."
	case errors.Is(err, workspace.ErrTerminalNotAvailable):
		return "Terminal access is not enabled for this workspace template."
	case errors.Is(err, workspace.ErrTerminalIdentityNotAllowed):
		return "That terminal identity is not allowed by the workspace template."
	default:
		return "The terminal session could not be opened."
	}
}

func desktopErrorText(err error) string {
	switch {
	case errors.Is(err, workspace.ErrRuntimeUnavailable), errors.Is(err, runtime.ErrUnavailable):
		return "The container runtime is unavailable."
	case errors.Is(err, workspace.ErrWorkspaceStateConflict), errors.Is(err, runtime.ErrConflict):
		return "The workspace must be running with an approved desktop service."
	case errors.Is(err, workspace.ErrDesktopNotAvailable):
		return "Desktop access is not enabled or configured for this workspace template."
	default:
		return "The graphical desktop session could not be opened."
	}
}

func workspaceActionVerb(action string) string {
	switch action {
	case "start":
		return "started"
	case "stop":
		return "stopped"
	case "restart":
		return "restarted"
	case "delete":
		return "deleted"
	default:
		return action
	}
}

func quotaFormError(err error) string {
	if errors.Is(err, quota.ErrInvalidQuota) {
		return "Enter zero or positive CPU, memory, storage, and workspace limits within the allowed ranges. Zero means unlimited."
	}
	return "The quota could not be saved."
}

func settingsFormError(err error) string {
	if errors.Is(err, quota.ErrInvalidQuota) {
		return "Enter CPU and memory overbooking factors between 0.1 and 1000 and valid storage values. Reserved storage cannot exceed configured storage capacity."
	}
	return "The host settings could not be saved."
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

// ready reports GET /readyz: a bounded readiness signal that checks both
// SQLite and rootless-Podman connectivity, unlike the liveness-only /healthz.
// A reverse proxy or process supervisor should use this endpoint, not
// /healthz, to decide whether to admit traffic.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	snapshot := s.readiness(r.Context())
	status := http.StatusOK
	if snapshot.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Status   string `json:"status"`
		Database string `json:"database"`
		Runtime  string `json:"runtime"`
	}{Status: snapshot.Status, Database: snapshot.Database, Runtime: snapshot.Runtime})
}

func (s *Server) readiness(ctx context.Context) readinessSnapshot {
	checkCtx, cancel := context.WithTimeout(ctx, readinessCheckTimeout)
	defer cancel()
	snapshot := readinessSnapshot{Status: "ok", Database: "ok", Runtime: "ok", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := s.db.PingContext(checkCtx); err != nil {
		snapshot.Status = "degraded"
		snapshot.Database = "unavailable"
	}
	if s.runtime == nil {
		snapshot.Status = "degraded"
		snapshot.Runtime = "unavailable"
	} else if _, err := s.runtime.Name(checkCtx); err != nil {
		snapshot.Status = "degraded"
		snapshot.Runtime = "unavailable"
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
