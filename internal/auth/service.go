package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidInput       = errors.New("invalid user input")
	ErrLastAdministrator  = errors.New("cannot disable the last active administrator")
	ErrSelfDisable        = errors.New("administrator cannot disable their own account")
)

const (
	auditLoginSuccess = "login.success"
	auditLoginFailure = "login.failure"
	auditUserCreated  = "user.created"
	auditUserDisabled = "user.disabled"
	auditUserEnabled  = "user.enabled"
)

type Service struct {
	store           repository.Store
	sessionLifetime time.Duration
	now             func() time.Time
}

type CreateUserInput struct {
	Username    string
	DisplayName string
	Password    string
	Role        domain.Role
}

func New(store repository.Store, sessionLifetime time.Duration) (*Service, error) {
	if sessionLifetime <= 0 {
		return nil, errors.New("session lifetime must be positive")
	}
	return &Service{store: store, sessionLifetime: sessionLifetime, now: time.Now}, nil
}

func (s *Service) BootstrapAdministrator(ctx context.Context, input CreateUserInput) (bool, error) {
	count, err := s.store.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}
	input.Role = domain.RoleAdministrator
	if _, err := s.createUser(ctx, input); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) Authenticate(ctx context.Context, username, password string) (domain.User, string, error) {
	username = normalizeUsername(username)
	record, err := s.store.FindUserByUsername(ctx, username)
	if err != nil || record.User.Disabled || bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(password)) != nil {
		s.recordAudit(ctx, domain.AuditEvent{EventType: auditLoginFailure, TargetType: "user", Metadata: map[string]string{"username": username}})
		return domain.User{}, "", ErrInvalidCredentials
	}

	rawToken, err := randomToken()
	if err != nil {
		return domain.User{}, "", fmt.Errorf("create session token: %w", err)
	}
	now := s.now().UTC()
	if err := s.store.CreateSession(ctx, domain.Session{
		TokenHash: hashToken(rawToken),
		UserID:    record.User.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(s.sessionLifetime),
		LastSeen:  now,
	}); err != nil {
		return domain.User{}, "", err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: record.User.ID, EventType: auditLoginSuccess, TargetType: "user", TargetID: record.User.ID})
	return record.User, rawToken, nil
}

func (s *Service) UserForSession(ctx context.Context, rawToken string) (domain.User, error) {
	if rawToken == "" {
		return domain.User{}, repository.ErrNotFound
	}
	return s.store.FindSessionUser(ctx, hashToken(rawToken), s.now().UTC().Unix())
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	return s.store.DeleteSession(ctx, hashToken(rawToken))
}

func (s *Service) ListUsers(ctx context.Context, actorID string) ([]domain.User, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.store.ListUsers(ctx)
}

func (s *Service) CreateUser(ctx context.Context, actorID string, input CreateUserInput) (domain.User, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.User{}, err
	}
	user, err := s.createUser(ctx, input)
	if err != nil {
		return domain.User{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditUserCreated, TargetType: "user", TargetID: user.ID, Metadata: map[string]string{"role": string(user.Role)}})
	return user, nil
}

func (s *Service) SetUserDisabled(ctx context.Context, actorID, targetID string, disabled bool) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if actorID == targetID && disabled {
		return ErrSelfDisable
	}
	target, err := s.store.FindUserByID(ctx, targetID)
	if err != nil {
		return err
	}
	if disabled && target.Role == domain.RoleAdministrator && !target.Disabled {
		count, err := s.store.CountActiveAdministrators(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastAdministrator
		}
	}
	if err := s.store.SetUserDisabled(ctx, targetID, disabled); err != nil {
		return err
	}
	eventType := auditUserEnabled
	if disabled {
		eventType = auditUserDisabled
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: eventType, TargetType: "user", TargetID: targetID})
	return nil
}

func (s *Service) requireAdministrator(ctx context.Context, actorID string) (domain.User, error) {
	user, err := s.store.FindUserByID(ctx, actorID)
	if err != nil {
		return domain.User{}, err
	}
	if !user.IsAdministrator() {
		return domain.User{}, ErrInvalidCredentials
	}
	return user, nil
}

func (s *Service) createUser(ctx context.Context, input CreateUserInput) (domain.User, error) {
	username := normalizeUsername(input.Username)
	if !validUsername(username) || !validDisplayName(input.DisplayName) || !validPassword(input.Password) || !input.Role.Valid() {
		return domain.User{}, ErrInvalidInput
	}
	if input.DisplayName == "" {
		input.DisplayName = username
	}
	if _, err := s.store.FindUserByUsername(ctx, username); err == nil {
		return domain.User{}, repository.ErrConflict
	} else if !errors.Is(err, repository.ErrNotFound) {
		return domain.User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}
	id, err := randomToken()
	if err != nil {
		return domain.User{}, fmt.Errorf("create user ID: %w", err)
	}
	now := s.now().UTC()
	user := domain.User{ID: id, Username: username, DisplayName: input.DisplayName, Role: input.Role, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateUser(ctx, user, string(hash)); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Service) recordAudit(ctx context.Context, event domain.AuditEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now().UTC()
	}
	_ = s.store.RecordAuditEvent(ctx, event)
}

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validUsername(value string) bool {
	if len(value) < 3 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (index > 0 && (char == '.' || char == '_' || char == '-')) {
			continue
		}
		return false
	}
	return true
}

func validDisplayName(value string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(value)) <= 120
}

func validPassword(value string) bool {
	return len(value) >= 12 && len(value) <= 72 && utf8.ValidString(value)
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func hashToken(raw string) string {
	hash := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
