package repository

import (
	"context"
	"errors"
	"time"

	"github.com/cows-project/cows/internal/domain"
)

var (
	ErrNotFound = errors.New("repository: not found")
	ErrConflict = errors.New("repository: conflict")
)

type UserRecord struct {
	User         domain.User
	PasswordHash string
}

// UserImportEntry is an administrator-approved account change. Existing
// users have a non-empty User.ID and retain their password and role.
type UserImportEntry struct {
	User         domain.User
	PasswordHash string
	GroupIDs     []string
	Existing     bool
}

type UserRepository interface {
	CountUsers(ctx context.Context) (int, error)
	CountActiveAdministrators(ctx context.Context) (int, error)
	FindUserByUsername(ctx context.Context, username string) (UserRecord, error)
	FindUserByEmail(ctx context.Context, email string) (UserRecord, error)
	FindUserByID(ctx context.Context, id string) (domain.User, error)
	FindUserCredentialsByID(ctx context.Context, id string) (UserRecord, error)
	ListUsers(ctx context.Context) ([]domain.User, error)
	CreateUser(ctx context.Context, user domain.User, passwordHash string) error
	ImportUsers(ctx context.Context, entries []UserImportEntry) error
	RegisterUser(ctx context.Context, user domain.User, passwordHash string, groupIDs []string, userQuota domain.UserQuota) error
	DeleteUser(ctx context.Context, id string) error
	UpdateUserPassword(ctx context.Context, id, passwordHash string, mustChangePassword bool) error
	ResetPasswordUsingToken(ctx context.Context, tokenHash, passwordHash string, now time.Time) (domain.User, error)
	SetUserDisabled(ctx context.Context, id string, disabled bool) error
	ListUserGroupIDs(ctx context.Context, userID string) ([]string, error)
	SetUserGroups(ctx context.Context, userID string, groupIDs []string) error
}

type GroupRepository interface {
	ListGroups(ctx context.Context) ([]domain.Group, error)
	FindGroupByID(ctx context.Context, id string) (domain.Group, error)
	FindGroupByName(ctx context.Context, name string) (domain.Group, error)
	CreateGroup(ctx context.Context, group domain.Group) error
	DeleteGroup(ctx context.Context, id string) error
}

type SessionRepository interface {
	CreateSession(ctx context.Context, session domain.Session) error
	FindSessionUser(ctx context.Context, tokenHash string, nowUnix int64) (domain.User, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteSessionsForUser(ctx context.Context, userID string) error
	DeleteSessionsForUserExcept(ctx context.Context, userID, keepTokenHash string) error
	DeleteExpiredSessions(ctx context.Context, nowUnix int64) error
}

type AuditRepository interface {
	RecordAuditEvent(ctx context.Context, event domain.AuditEvent) error
	ListAuditEvents(ctx context.Context, query domain.AuditQuery) ([]domain.AuditRecord, error)
}

type PasswordResetRepository interface {
	CreatePasswordResetToken(ctx context.Context, token domain.PasswordResetToken) error
}

type PasswordResetEmailRepository interface {
	UpsertPasswordResetEmail(ctx context.Context, email domain.PasswordResetEmail) error
	ListPendingPasswordResetEmails(ctx context.Context, now time.Time, limit int) ([]domain.PasswordResetEmail, error)
	MarkPasswordResetEmailSent(ctx context.Context, id int64, sentAt time.Time) error
	MarkPasswordResetEmailFailed(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, errorCode string) error
	MarkPasswordResetEmailCanceled(ctx context.Context, id int64) error
}

type NotificationRepository interface {
	UpsertEmailNotification(ctx context.Context, notification domain.EmailNotification) error
	ListPendingEmailNotifications(ctx context.Context, now time.Time, limit int) ([]domain.EmailNotification, error)
	MarkEmailNotificationSent(ctx context.Context, id int64, sentAt time.Time) error
	MarkEmailNotificationFailed(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, errorCode string) error
	MarkEmailNotificationCanceled(ctx context.Context, id int64) error
	CancelEmailNotificationsForWorkspace(ctx context.Context, workspaceID string) error
	CancelEmailNotificationsForUser(ctx context.Context, userID string) error
}

type TemplateRepository interface {
	ListTemplates(ctx context.Context) ([]domain.WorkspaceTemplate, error)
	FindTemplateByID(ctx context.Context, id string) (domain.WorkspaceTemplate, error)
	FindTemplateByName(ctx context.Context, name string) (domain.WorkspaceTemplate, error)
	CreateTemplate(ctx context.Context, template domain.WorkspaceTemplate) error
	UpdateTemplate(ctx context.Context, template domain.WorkspaceTemplate) error
	SetTemplateEnabled(ctx context.Context, id string, enabled bool, updatedAt time.Time) error
}

type PortAllocationRepository interface {
	ReserveWorkspacePort(ctx context.Context, allocation domain.PortAllocation) error
	ListWorkspacePortAllocations(ctx context.Context, workspaceID string) ([]domain.PortAllocation, error)
	ReleaseWorkspacePorts(ctx context.Context, workspaceID string) error
}

type WorkspaceRepository interface {
	ListWorkspacesForUser(ctx context.Context, ownerUserID string) ([]domain.Workspace, error)
	ListAllWorkspaces(ctx context.Context) ([]domain.Workspace, error)
	FindWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error)
	FindWorkspaceByOwnerAndName(ctx context.Context, ownerUserID, name string) (domain.Workspace, error)
	CreateWorkspace(ctx context.Context, workspace domain.Workspace) error
	DeleteWorkspace(ctx context.Context, id string) error
	DeleteWorkspaceRetainingStorage(ctx context.Context, id string, volumes []domain.RetainedWorkspaceVolume, directory *domain.RetainedWorkspaceDirectory) error
	ListRetainedWorkspaceVolumes(ctx context.Context, workspaceID string) ([]domain.RetainedWorkspaceVolume, error)
	ListAllRetainedWorkspaceVolumes(ctx context.Context) ([]domain.RetainedWorkspaceVolume, error)
	ListRetainedWorkspaceVolumesForOwner(ctx context.Context, ownerUserID string) ([]domain.RetainedWorkspaceVolume, error)
	DeleteRetainedWorkspaceVolume(ctx context.Context, volumeName string) error
	// ConsumeRetainedWorkspaceVolume looks up the tombstone for (workspaceID,
	// mountName) scoped to ownerUserID and deletes it in the same call,
	// claiming it for reattachment. Returns repository.ErrNotFound if the row
	// does not exist or is not owned by ownerUserID (the two cases are
	// indistinguishable to the caller, deliberately).
	ConsumeRetainedWorkspaceVolume(ctx context.Context, workspaceID, mountName, ownerUserID string) (domain.RetainedWorkspaceVolume, error)
	// FindRetainedWorkspaceVolume is the read-only counterpart of
	// ConsumeRetainedWorkspaceVolume, for self-service download.
	FindRetainedWorkspaceVolume(ctx context.Context, workspaceID, mountName, ownerUserID string) (domain.RetainedWorkspaceVolume, error)
	ListRetainedWorkspaceDirectoriesForOwner(ctx context.Context, ownerUserID string) ([]domain.RetainedWorkspaceDirectory, error)
	DeleteRetainedWorkspaceDirectory(ctx context.Context, workspaceID string) error
	// ConsumeRetainedWorkspaceDirectory is the directory equivalent of
	// ConsumeRetainedWorkspaceVolume.
	ConsumeRetainedWorkspaceDirectory(ctx context.Context, workspaceID, ownerUserID string) (domain.RetainedWorkspaceDirectory, error)
	// FindRetainedWorkspaceDirectory is the read-only counterpart, for
	// self-service download.
	FindRetainedWorkspaceDirectory(ctx context.Context, workspaceID, ownerUserID string) (domain.RetainedWorkspaceDirectory, error)
	SetWorkspaceDesiredState(ctx context.Context, id string, state domain.DesiredWorkspaceState, updatedAt time.Time) error
	UpdateWorkspaceObservedState(ctx context.Context, id, observedState, runtimeID, observedErrorCode, observedError string, observedAt, updatedAt time.Time) error
	UpdateWorkspaceLifecycle(ctx context.Context, id string, startedAt, lastConnectedAt, stoppedAt, containerDeletedAt, dataArchiveEligibleAt, updatedAt time.Time) error
	UpdateWorkspaceOperation(ctx context.Context, id, operation, status, operationError string, startedAt, updatedAt time.Time) error
	WorkspaceAllocations(ctx context.Context, ownerUserID string) (domain.AllocationSummary, error)
	AllWorkspaceAllocations(ctx context.Context) (domain.AllocationSummary, error)
}

type QuotaRepository interface {
	FindUserQuota(ctx context.Context, userID string) (domain.UserQuota, error)
	ListUserQuotas(ctx context.Context) ([]domain.UserQuota, error)
	UpsertUserQuota(ctx context.Context, quota domain.UserQuota) error
	DeleteUserQuota(ctx context.Context, userID string) error
	FindGroupQuota(ctx context.Context, groupID string) (domain.GroupQuota, error)
	ListGroupQuotas(ctx context.Context) ([]domain.GroupQuota, error)
	ListGroupQuotasForUser(ctx context.Context, userID string) ([]domain.GroupQuota, error)
	UpsertGroupQuota(ctx context.Context, quota domain.GroupQuota) error
	DeleteGroupQuota(ctx context.Context, groupID string) error
}

type HostSettingsRepository interface {
	FindHostSettings(ctx context.Context) (domain.HostSettings, error)
	UpsertHostSettings(ctx context.Context, settings domain.HostSettings) error
}

type Store interface {
	UserRepository
	SessionRepository
	AuditRepository
	TemplateRepository
	GroupRepository
	PortAllocationRepository
	WorkspaceRepository
	QuotaRepository
	HostSettingsRepository
	NotificationRepository
	PasswordResetRepository
	PasswordResetEmailRepository
}
