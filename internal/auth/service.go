package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
)

var (
	ErrInvalidCredentials      = errors.New("invalid credentials")
	ErrInvalidInput            = errors.New("invalid user input")
	ErrPasswordChangeRequired  = errors.New("password change required")
	ErrLastAdministrator       = errors.New("cannot disable the last active administrator")
	ErrSelfDisable             = errors.New("administrator cannot disable their own account")
	ErrInvalidGroup            = errors.New("invalid group")
	ErrRegistrationDisabled    = errors.New("registration is disabled")
	ErrRegistrationUnavailable = errors.New("registration is unavailable")
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
	registration    RegistrationPolicy
	now             func() time.Time
}

type RegistrationPolicy struct {
	Enabled           bool
	DefaultGroupNames []string
	DefaultQuota      domain.UserQuota
}

type CreateUserInput struct {
	Username    string
	Email       string
	DisplayName string
	Password    string
	Role        domain.Role
}

type RegisterUserInput struct {
	Username             string
	Email                string
	DisplayName          string
	Password             string
	PasswordConfirmation string
}

func New(store repository.Store, sessionLifetime time.Duration, policies ...RegistrationPolicy) (*Service, error) {
	if sessionLifetime <= 0 {
		return nil, errors.New("session lifetime must be positive")
	}
	policy := RegistrationPolicy{}
	if len(policies) > 1 {
		return nil, errors.New("at most one registration policy is supported")
	}
	if len(policies) == 1 {
		policy = policies[0]
	}
	seenGroups := make(map[string]struct{}, len(policy.DefaultGroupNames))
	for _, name := range policy.DefaultGroupNames {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("registration default group names must not be empty")
		}
		key := strings.ToLower(name)
		if _, exists := seenGroups[key]; exists {
			return nil, fmt.Errorf("registration default group %q is repeated", name)
		}
		seenGroups[key] = struct{}{}
	}
	policy.DefaultGroupNames = append([]string(nil), policy.DefaultGroupNames...)
	return &Service{store: store, sessionLifetime: sessionLifetime, registration: policy, now: time.Now}, nil
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

func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	record, err := s.store.FindUserCredentialsByID(ctx, userID)
	if err != nil {
		return err
	}
	if record.User.Disabled || bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(currentPassword)) != nil {
		return ErrInvalidCredentials
	}
	if !validPassword(newPassword) {
		return ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.store.UpdateUserPassword(ctx, userID, string(hash), false); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: userID, EventType: "password.changed", TargetType: "user", TargetID: userID})
	return nil
}

func (s *Service) Register(ctx context.Context, input RegisterUserInput) (domain.User, error) {
	if !s.registration.Enabled {
		return domain.User{}, ErrRegistrationDisabled
	}
	if input.Password != input.PasswordConfirmation || strings.TrimSpace(input.Email) == "" {
		return domain.User{}, ErrInvalidInput
	}
	user, passwordHash, err := s.prepareUser(ctx, CreateUserInput{
		Username: input.Username, Email: input.Email, DisplayName: input.DisplayName,
		Password: input.Password, Role: domain.RoleUser,
	}, false)
	if err != nil {
		return domain.User{}, err
	}
	groupIDs := make([]string, 0, len(s.registration.DefaultGroupNames))
	for _, name := range s.registration.DefaultGroupNames {
		group, err := s.store.FindGroupByName(ctx, name)
		if errors.Is(err, repository.ErrNotFound) {
			return domain.User{}, ErrRegistrationUnavailable
		}
		if err != nil {
			return domain.User{}, ErrRegistrationUnavailable
		}
		groupIDs = append(groupIDs, group.ID)
	}
	now := s.now().UTC()
	userQuota := s.registration.DefaultQuota
	userQuota.UserID = user.ID
	userQuota.CreatedAt = now
	userQuota.UpdatedAt = now
	if err := s.store.RegisterUser(ctx, user, passwordHash, groupIDs, userQuota); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.User{}, repository.ErrConflict
		}
		return domain.User{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{EventType: "user.registered", TargetType: "user", TargetID: user.ID})
	return user, nil
}

func (s *Service) ListUsers(ctx context.Context, actorID string) ([]domain.User, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.store.ListUsers(ctx)
}

func (s *Service) FindUserForAdmin(ctx context.Context, actorID, userID string) (domain.User, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.User{}, err
	}
	return s.store.FindUserByID(ctx, userID)
}

func (s *Service) ListGroups(ctx context.Context, actorID string) ([]domain.Group, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.store.ListGroups(ctx)
}

func (s *Service) CreateGroup(ctx context.Context, actorID, name, description string) (domain.Group, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.Group{}, err
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(description) > 2000 {
		return domain.Group{}, ErrInvalidGroup
	}
	for _, value := range name {
		if value < 0x20 || value == 0x7f {
			return domain.Group{}, ErrInvalidGroup
		}
	}
	id, err := randomToken()
	if err != nil {
		return domain.Group{}, err
	}
	now := s.now().UTC()
	group := domain.Group{ID: id, Name: name, Description: description, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateGroup(ctx, group); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.Group{}, repository.ErrConflict
		}
		return domain.Group{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "group.created", TargetType: "group", TargetID: id})
	return group, nil
}

func (s *Service) UserGroupIDs(ctx context.Context, actorID, userID string) ([]string, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	if _, err := s.store.FindUserByID(ctx, userID); err != nil {
		return nil, err
	}
	return s.store.ListUserGroupIDs(ctx, userID)
}

func (s *Service) SetUserGroups(ctx context.Context, actorID, userID string, groupIDs []string) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if _, err := s.store.FindUserByID(ctx, userID); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if _, ok := seen[groupID]; ok {
			return ErrInvalidGroup
		}
		seen[groupID] = struct{}{}
		if _, err := s.store.FindGroupByID(ctx, groupID); err != nil {
			return err
		}
	}
	if err := s.store.SetUserGroups(ctx, userID, groupIDs); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.groups_updated", TargetType: "user", TargetID: userID})
	return nil
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
	if user.MustChangePassword {
		return domain.User{}, ErrPasswordChangeRequired
	}
	return user, nil
}

func (s *Service) createUser(ctx context.Context, input CreateUserInput) (domain.User, error) {
	user, hash, err := s.prepareUser(ctx, input, true)
	if err != nil {
		return domain.User{}, err
	}
	if err := s.store.CreateUser(ctx, user, hash); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

func (s *Service) prepareUser(ctx context.Context, input CreateUserInput, mustChangePassword bool) (domain.User, string, error) {
	username := normalizeUsername(input.Username)
	if !validUsername(username) || !validEmail(input.Email) || !validDisplayName(input.DisplayName) || !validPassword(input.Password) || !input.Role.Valid() {
		return domain.User{}, "", ErrInvalidInput
	}
	if input.DisplayName == "" {
		input.DisplayName = username
	}
	if _, err := s.store.FindUserByUsername(ctx, username); err == nil {
		return domain.User{}, "", repository.ErrConflict
	} else if !errors.Is(err, repository.ErrNotFound) {
		return domain.User{}, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return domain.User{}, "", fmt.Errorf("hash password: %w", err)
	}
	id, err := randomToken()
	if err != nil {
		return domain.User{}, "", fmt.Errorf("create user ID: %w", err)
	}
	now := s.now().UTC()
	user := domain.User{ID: id, Username: username, Email: strings.TrimSpace(input.Email), DisplayName: input.DisplayName, Role: input.Role, MustChangePassword: mustChangePassword, CreatedAt: now, UpdatedAt: now}
	return user, string(hash), nil
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

func validEmail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value && len(value) <= 254
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
