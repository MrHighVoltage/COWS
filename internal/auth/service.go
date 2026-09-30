package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
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
	ErrSelfDelete              = errors.New("administrator cannot delete their own account")
	ErrInvalidGroup            = errors.New("invalid group")
	ErrGroupInUse              = errors.New("group is still referenced by a workspace template")
	ErrUserHasWorkspaces       = errors.New("user still owns workspaces")
	ErrUserMustBeDisabled      = errors.New("user must be disabled before deletion")
	ErrRegistrationDisabled    = errors.New("registration is disabled")
	ErrRegistrationUnavailable = errors.New("registration is unavailable")
	ErrInvalidResetToken       = errors.New("invalid or expired password reset token")
	ErrRecoveryTargetInvalid   = errors.New("recovery target must be an existing, enabled administrator")
	ErrEmailInUse              = errors.New("email address already belongs to another account")
	ErrTargetDisabled          = errors.New("the account must be enabled")
	ErrSelfPasswordSet         = errors.New("administrator cannot set their own password from the user editor")
)

const (
	auditLoginSuccess   = "login.success"
	auditLoginFailure   = "login.failure"
	auditUserCreated    = "user.created"
	auditUserDisabled   = "user.disabled"
	auditUserEnabled    = "user.enabled"
	auditAdminRecovered = "administrator.recovered"

	auditUserProfileUpdated = "user.profile_updated"
	auditUserPasswordSet    = "user.password_set"
)

type Service struct {
	store                    repository.Store
	sessionLifetime          time.Duration
	registration             RegistrationPolicy
	invitationsAvailable     bool
	invitationLifetime       time.Duration
	passwordResetLifetime    time.Duration
	passwordResetMinInterval time.Duration
	now                      func() time.Time
}

// SetInvitationPolicy enables password-less account creation. It stays off
// until the caller confirms email delivery and an external base URL are
// configured, because an account with no usable password and no way to reach
// its owner cannot be opened by anyone.
func (s *Service) SetInvitationPolicy(available bool, lifetime time.Duration) {
	s.invitationsAvailable = available
	if lifetime > 0 {
		s.invitationLifetime = lifetime
	}
}

// SetPasswordResetPolicy sets how long a reset link lives and how rarely one
// account can have a reset message queued.
func (s *Service) SetPasswordResetPolicy(lifetime, minInterval time.Duration) {
	if lifetime > 0 {
		s.passwordResetLifetime = lifetime
	}
	if minInterval > 0 {
		s.passwordResetMinInterval = minInterval
	}
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

type PasswordResetRequest struct {
	User      domain.User
	Token     string
	ExpiresAt time.Time
}

// Bounds a caller that never sets an explicit policy still gets.
const (
	defaultInvitationLifetime       = 24 * time.Hour
	defaultPasswordResetLifetime    = 2 * time.Hour
	defaultPasswordResetMinInterval = 5 * time.Minute
)

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
	normalizedGroups := make([]string, 0, len(policy.DefaultGroupNames))
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
		normalizedGroups = append(normalizedGroups, name)
	}
	policy.DefaultGroupNames = normalizedGroups
	return &Service{
		store: store, sessionLifetime: sessionLifetime, registration: policy,
		invitationLifetime:       defaultInvitationLifetime,
		passwordResetLifetime:    defaultPasswordResetLifetime,
		passwordResetMinInterval: defaultPasswordResetMinInterval,
		now:                      time.Now,
	}, nil
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
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.UpdateUserPassword(ctx, userID, hash, false); err != nil {
		return err
	}
	// A reset or invitation link mailed earlier must not survive the change it
	// was meant to bring about; the caller keeps its own session, so sessions
	// are left to RevokeOtherSessions.
	if err := s.store.InvalidateUserCredentialTokens(ctx, userID); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: userID, EventType: "password.changed", TargetType: "user", TargetID: userID})
	return nil
}

// RevokeOtherSessions deletes every session for userID except the one backing
// keepRawToken. It is called after a successful password change so that a
// stolen session cannot outlive the password change; the caller's own session
// is preserved so the first-login password-change flow stays logged in. A blank
// keepRawToken revokes all sessions for the user.
func (s *Service) RevokeOtherSessions(ctx context.Context, userID, keepRawToken string) error {
	return s.store.DeleteSessionsForUserExcept(ctx, userID, hashToken(keepRawToken))
}

// RequestPasswordReset deliberately returns no error for an unknown or
// disabled account. The caller can therefore show the same response for every
// identifier without allowing account enumeration.
func (s *Service) RequestPasswordReset(ctx context.Context, identifier string) (PasswordResetRequest, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || len(identifier) > 320 {
		return PasswordResetRequest{}, nil
	}
	record, err := s.store.FindUserByEmail(ctx, identifier)
	if errors.Is(err, repository.ErrNotFound) {
		record, err = s.store.FindUserByUsername(ctx, normalizeUsername(identifier))
	}
	if errors.Is(err, repository.ErrNotFound) || err != nil || record.User.Disabled || record.User.Email == "" {
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return PasswordResetRequest{}, err
		}
		return PasswordResetRequest{}, nil
	}
	now := s.now().UTC()
	// Throttle per account. The caller's response is identical either way, so
	// this cannot become an account-existence oracle.
	recent, err := s.store.CountRecentEmailMessages(ctx, record.User.ID, domain.EmailKindPasswordReset, now.Add(-s.passwordResetMinInterval))
	if err != nil {
		return PasswordResetRequest{}, err
	}
	if recent > 0 {
		return PasswordResetRequest{}, nil
	}
	rawToken, expiresAt, err := s.issueToken(ctx, record.User.ID, domain.TokenPurposeReset, s.passwordResetLifetime)
	if err != nil {
		return PasswordResetRequest{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{EventType: "password.reset.requested", TargetType: "user", TargetID: record.User.ID})
	return PasswordResetRequest{User: record.User, Token: rawToken, ExpiresAt: expiresAt}, nil
}

func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	// The password is judged before the token so a caller who fumbles both is
	// told about the part they can fix without learning anything about the token.
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if rawToken == "" {
		return ErrInvalidResetToken
	}
	user, err := s.store.ResetPasswordUsingToken(ctx, hashToken(rawToken), domain.TokenPurposeReset, hash, s.now().UTC())
	if errors.Is(err, repository.ErrNotFound) {
		return ErrInvalidResetToken
	}
	if err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: user.ID, EventType: "password.reset.completed", TargetType: "user", TargetID: user.ID})
	return nil
}

// RecoverAdministrator resets a named administrator's password to a generated
// temporary one, requires a change at the next login, and invalidates every
// session for the account. It is the offline recovery path for lost
// administrator credentials and is reachable only from the `cows recover-admin`
// subcommand, so its trust boundary is local access to the database file rather
// than any credential. That is also why it names the exact reason it refused,
// unlike RequestPasswordReset, whose deliberately vague response protects the
// unauthenticated web endpoint from account enumeration.
//
// A disabled administrator is refused: re-enabling an account is a separate,
// audited decision and must not happen as a side effect of a password reset.
// The plaintext password is returned to the caller to be shown once and is
// never stored or logged.
func (s *Service) RecoverAdministrator(ctx context.Context, username string) (string, error) {
	record, err := s.store.FindUserByUsername(ctx, normalizeUsername(username))
	if errors.Is(err, repository.ErrNotFound) {
		return "", ErrRecoveryTargetInvalid
	}
	if err != nil {
		return "", err
	}
	if record.User.Disabled || record.User.Role != domain.RoleAdministrator {
		return "", ErrRecoveryTargetInvalid
	}
	password, err := GenerateTemporaryPassword(20)
	if err != nil {
		return "", fmt.Errorf("generate temporary password: %w", err)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	if err := s.replaceCredential(ctx, record.User.ID, hash); err != nil {
		return "", err
	}
	s.recordAudit(ctx, domain.AuditEvent{EventType: auditAdminRecovered, TargetType: "user", TargetID: record.User.ID})
	return password, nil
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

func (s *Service) FindGroupForAdmin(ctx context.Context, actorID, groupID string) (domain.Group, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.Group{}, err
	}
	return s.store.FindGroupByID(ctx, groupID)
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
	current, err := s.store.ListUserGroupIDs(ctx, userID)
	if err != nil {
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
	removed := difference(current, groupIDs)
	added := difference(groupIDs, current)
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.groups_updated", TargetType: "user", TargetID: userID, Metadata: map[string]string{"added_group_ids": strings.Join(added, ","), "removed_group_ids": strings.Join(removed, ",")}})
	return nil
}

// RemoveUserFromGroup is the explicit single-membership operation used by
// administrator workflows. It preserves all other memberships and existing
// workspaces.
func (s *Service) RemoveUserFromGroup(ctx context.Context, actorID, userID, groupID string) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if _, err := s.store.FindUserByID(ctx, userID); err != nil {
		return err
	}
	if _, err := s.store.FindGroupByID(ctx, groupID); err != nil {
		return err
	}
	groupIDs, err := s.store.ListUserGroupIDs(ctx, userID)
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(groupIDs))
	for _, value := range groupIDs {
		if value != groupID {
			filtered = append(filtered, value)
		}
	}
	return s.SetUserGroups(ctx, actorID, userID, filtered)
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

// UpdateUserProfile lets an administrator correct an account's email address
// and display name. Username and role stay out of it deliberately: both change
// how the account authenticates or what it may do, and they have their own
// paths. Changing the email invalidates outstanding invitation and reset links
// for the account, because they were mailed to the previous address.
func (s *Service) UpdateUserProfile(ctx context.Context, actorID, targetID, email, displayName string) (domain.User, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.User{}, err
	}
	target, err := s.store.FindUserByID(ctx, targetID)
	if err != nil {
		return domain.User{}, err
	}
	email = strings.TrimSpace(email)
	displayName = strings.TrimSpace(displayName)
	if !validEmail(email) || !validDisplayName(displayName) {
		return domain.User{}, ErrInvalidInput
	}
	if available, err := s.emailAvailable(ctx, email, targetID); err != nil {
		return domain.User{}, err
	} else if !available {
		return domain.User{}, ErrEmailInUse
	}
	if displayName == "" {
		displayName = target.Username
	}
	if err := s.store.UpdateUserProfile(ctx, targetID, email, displayName, s.now().UTC()); err != nil {
		return domain.User{}, err
	}
	// The audit trail records that contact details changed, not the addresses
	// themselves; the current value is always readable from the account.
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditUserProfileUpdated, TargetType: "user", TargetID: targetID, Metadata: map[string]string{
		"email_changed": strconv.FormatBool(email != strings.TrimSpace(target.Email)),
	}})
	target.Email = email
	target.DisplayName = displayName
	return target, nil
}

// SetUserPassword replaces an account's password directly. It is the fallback
// for deployments without email delivery; where mail works, an invitation or
// reset link is preferable because it never puts the password in an
// administrator's hands. The account must change the password at next login,
// and every existing session for it is dropped.
func (s *Service) SetUserPassword(ctx context.Context, actorID, targetID, newPassword string) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	// The administrator's own password has its own path, which proves knowledge
	// of the current one. Routing it through here instead would end the session
	// making the request and force the actor back through a password change.
	if actorID == targetID {
		return ErrSelfPasswordSet
	}
	target, err := s.store.FindUserByID(ctx, targetID)
	if err != nil {
		return err
	}
	// Every other credential operation refuses a disabled account, because
	// re-opening one is a separate, audited decision and must not happen as a
	// side effect of handing out a password.
	if target.Disabled {
		return ErrTargetDisabled
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.replaceCredential(ctx, targetID, hash); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditUserPasswordSet, TargetType: "user", TargetID: targetID})
	return nil
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

func (s *Service) DeleteUser(ctx context.Context, actorID, targetID string) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if actorID == targetID {
		return ErrSelfDelete
	}
	target, err := s.store.FindUserByID(ctx, targetID)
	if err != nil {
		return err
	}
	if !target.Disabled {
		return ErrUserMustBeDisabled
	}
	// Defense in depth: today the only caller (adminUserDelete) always calls
	// DeleteUserWorkspaces first and checks its error, but workspaces.owner_user_id
	// is ON DELETE CASCADE - a future caller that skips or reorders that step
	// would otherwise have the database cascade silently delete workspace rows,
	// bypassing DeleteWorkspace's container removal, directory archival, and
	// retained-storage tombstoning entirely.
	if workspaces, err := s.store.ListWorkspacesForUser(ctx, targetID); err != nil {
		return err
	} else if len(workspaces) > 0 {
		return ErrUserHasWorkspaces
	}
	if err := s.store.CancelEmailMessagesForUser(ctx, targetID); err != nil {
		return err
	}
	if err := s.store.DeleteUser(ctx, targetID); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.deleted", TargetType: "user", TargetID: targetID})
	return nil
}

func (s *Service) DeleteGroup(ctx context.Context, actorID, groupID string) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if _, err := s.store.FindGroupByID(ctx, groupID); err != nil {
		return err
	}
	templates, err := s.store.ListTemplates(ctx)
	if err != nil {
		return err
	}
	for _, value := range templates {
		for _, allowedGroupID := range value.AllowedGroupIDs {
			if allowedGroupID == groupID {
				return ErrGroupInUse
			}
		}
	}
	if err := s.store.DeleteGroup(ctx, groupID); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "group.deleted", TargetType: "group", TargetID: groupID})
	return nil
}

func difference(left, right []string) []string {
	other := make(map[string]struct{}, len(right))
	for _, value := range right {
		other[value] = struct{}{}
	}
	result := make([]string, 0)
	for _, value := range left {
		if _, ok := other[value]; !ok {
			result = append(result, value)
		}
	}
	return result
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
	if available, err := s.emailAvailable(ctx, input.Email, ""); err != nil {
		return domain.User{}, "", err
	} else if !available {
		return domain.User{}, "", ErrEmailInUse
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

// issueToken stores the hash of a fresh single-use credential token and returns
// the raw value, which belongs only in a link. Every credential link in COWS is
// issued here so the three call sites cannot drift apart on lifetime, hashing,
// or purpose scoping. CreatePasswordResetToken replaces the account's previous
// token of the same purpose and cancels any queued message carrying it.
func (s *Service) issueToken(ctx context.Context, userID, purpose string, lifetime time.Duration) (string, time.Time, error) {
	rawToken, err := randomToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create %s token: %w", purpose, err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(lifetime)
	if err := s.store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{
		TokenHash: hashToken(rawToken), UserID: userID, Purpose: purpose,
		ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return "", time.Time{}, err
	}
	return rawToken, expiresAt, nil
}

// hashPassword validates a candidate password and returns its bcrypt hash.
func hashPassword(value string) (string, error) {
	if !validPassword(value) {
		return "", ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// replaceCredential installs a password that someone other than the account
// holder chose. All three effects belong together: the account must pick its own
// password at the next sign-in, no session may outlive the credential it was
// opened with, and any invitation or reset link mailed earlier must stop working
// now that the credential has been replaced by another route.
func (s *Service) replaceCredential(ctx context.Context, userID, passwordHash string) error {
	if err := s.store.UpdateUserPassword(ctx, userID, passwordHash, true); err != nil {
		return err
	}
	if err := s.store.DeleteSessionsForUser(ctx, userID); err != nil {
		return err
	}
	return s.store.InvalidateUserCredentialTokens(ctx, userID)
}

// emailAvailable reports whether an address is free, or already belongs to
// userID. Addresses must stay unique because the unauthenticated reset endpoint
// resolves an identifier to a single account: were two accounts to share an
// address, whoever reads that mailbox could reset either one, including an
// administrator account they do not own.
func (s *Service) emailAvailable(ctx context.Context, email, userID string) (bool, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return true, nil
	}
	record, err := s.store.FindUserByEmail(ctx, email)
	if errors.Is(err, repository.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return record.User.ID == userID, nil
}

// SendPasswordResetFor issues a reset token on an administrator's behalf, so
// no secret has to be relayed out of band. Unlike RequestPasswordReset it names
// the exact reason it refused: the caller is an authenticated administrator
// acting on a user they can already see, so there is nothing to protect from
// enumeration here, and it is deliberately not throttled for the same reason.
// RecoverAdministrator remains for deployments without email.
func (s *Service) SendPasswordResetFor(ctx context.Context, actorID, targetUserID string) (PasswordResetRequest, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return PasswordResetRequest{}, err
	}
	target, err := s.store.FindUserByID(ctx, targetUserID)
	if err != nil {
		return PasswordResetRequest{}, err
	}
	if target.Disabled || strings.TrimSpace(target.Email) == "" {
		return PasswordResetRequest{}, ErrRecoveryTargetInvalid
	}
	rawToken, expiresAt, err := s.issueToken(ctx, target.ID, domain.TokenPurposeReset, s.passwordResetLifetime)
	if err != nil {
		return PasswordResetRequest{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.password_reset_sent", TargetType: "user", TargetID: target.ID})
	return PasswordResetRequest{User: target, Token: rawToken, ExpiresAt: expiresAt}, nil
}
