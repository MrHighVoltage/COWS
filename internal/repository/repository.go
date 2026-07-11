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

type UserRepository interface {
	CountUsers(ctx context.Context) (int, error)
	CountActiveAdministrators(ctx context.Context) (int, error)
	FindUserByUsername(ctx context.Context, username string) (UserRecord, error)
	FindUserByID(ctx context.Context, id string) (domain.User, error)
	ListUsers(ctx context.Context) ([]domain.User, error)
	CreateUser(ctx context.Context, user domain.User, passwordHash string) error
	SetUserDisabled(ctx context.Context, id string, disabled bool) error
}

type SessionRepository interface {
	CreateSession(ctx context.Context, session domain.Session) error
	FindSessionUser(ctx context.Context, tokenHash string, nowUnix int64) (domain.User, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteExpiredSessions(ctx context.Context, nowUnix int64) error
}

type AuditRepository interface {
	RecordAuditEvent(ctx context.Context, event domain.AuditEvent) error
}

type TemplateRepository interface {
	ListTemplates(ctx context.Context) ([]domain.WorkspaceTemplate, error)
	FindTemplateByID(ctx context.Context, id string) (domain.WorkspaceTemplate, error)
	FindTemplateByName(ctx context.Context, name string) (domain.WorkspaceTemplate, error)
	CreateTemplate(ctx context.Context, template domain.WorkspaceTemplate) error
	UpdateTemplate(ctx context.Context, template domain.WorkspaceTemplate) error
	SetTemplateEnabled(ctx context.Context, id string, enabled bool, updatedAt time.Time) error
}

type Store interface {
	UserRepository
	SessionRepository
	AuditRepository
	TemplateRepository
}
