package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/cows-project/cows/internal/domain"
)

// ErrInvitationUnavailable means an account was requested without a password
// but cannot be invited: there is no address to send to, or email delivery is
// not configured. Creating an account nobody can reach and nobody can log into
// must fail rather than succeed silently.
var ErrInvitationUnavailable = errors.New("invitation cannot be delivered")

type InvitationRequest struct {
	User      domain.User
	Token     string
	ExpiresAt time.Time
}

// unusablePasswordHash returns a bcrypt hash of a random value that is
// immediately discarded, so no password can ever match it. This is
// deliberately not an empty string or a fixed sentinel: either would be a
// guessable secret shared across every invited account.
func unusablePasswordHash() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate placeholder secret: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(base64.RawURLEncoding.EncodeToString(secret)), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash placeholder secret: %w", err)
	}
	return string(hash), nil
}

// issueInvitationToken stores the hash of a fresh single-use invitation and
// returns the raw value, which belongs only in a link.
func (s *Service) issueInvitationToken(ctx context.Context, userID string) (string, time.Time, error) {
	rawToken, err := randomToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create invitation token: %w", err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.invitationLifetime)
	if err := s.store.CreatePasswordResetToken(ctx, domain.PasswordResetToken{
		TokenHash: hashToken(rawToken), UserID: userID, Purpose: domain.TokenPurposeInvitation,
		ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return "", time.Time{}, err
	}
	return rawToken, expiresAt, nil
}

// CreateUserWithInvitation creates an account with a password when one is
// given, and otherwise an account that can only be opened through a mailed
// single-use invitation. The returned token is raw and must be placed only in
// a link; only its hash is stored.
func (s *Service) CreateUserWithInvitation(ctx context.Context, actorID string, input CreateUserInput) (domain.User, InvitationRequest, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	if strings.TrimSpace(input.Password) != "" {
		user, err := s.createUser(ctx, input)
		if err != nil {
			return domain.User{}, InvitationRequest{}, err
		}
		s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditUserCreated, TargetType: "user", TargetID: user.ID, Metadata: map[string]string{"role": string(user.Role)}})
		return user, InvitationRequest{}, nil
	}
	if !s.invitationsAvailable || strings.TrimSpace(input.Email) == "" {
		return domain.User{}, InvitationRequest{}, ErrInvitationUnavailable
	}
	// prepareUser validates the password it is handed, so give it a value that
	// passes and then discard the resulting hash for an unusable one.
	placeholder, err := GenerateTemporaryPassword(20)
	if err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	input.Password = placeholder
	user, _, err := s.prepareUser(ctx, input, true)
	if err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	hash, err := unusablePasswordHash()
	if err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	if err := s.store.CreateUser(ctx, user, hash); err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	rawToken, expiresAt, err := s.issueInvitationToken(ctx, user.ID)
	if err != nil {
		return domain.User{}, InvitationRequest{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditUserCreated, TargetType: "user", TargetID: user.ID, Metadata: map[string]string{"role": string(user.Role), "credential": "invitation"}})
	return user, InvitationRequest{User: user, Token: rawToken, ExpiresAt: expiresAt}, nil
}

// AcceptInvitation consumes a single-use invitation token, sets the first
// password, clears the forced password change, and invalidates every session
// for the account. It mirrors the reset path deliberately: the only
// differences are the token purpose and the audit event.
func (s *Service) AcceptInvitation(ctx context.Context, rawToken, password, confirmation string) (domain.User, error) {
	if password != confirmation || !validPassword(password) {
		return domain.User{}, ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}
	user, err := s.store.ResetPasswordUsingToken(ctx, hashToken(rawToken), domain.TokenPurposeInvitation, string(hash), s.now().UTC())
	if err != nil {
		return domain.User{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{EventType: "user.invitation_accepted", TargetType: "user", TargetID: user.ID})
	return user, nil
}

// ResendInvitation issues a fresh invitation. CreatePasswordResetToken deletes
// the account's previous tokens of the same purpose and cancels any queued
// message still carrying them, so the superseded link stops working.
func (s *Service) ResendInvitation(ctx context.Context, actorID, targetUserID string) (InvitationRequest, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return InvitationRequest{}, err
	}
	if !s.invitationsAvailable {
		return InvitationRequest{}, ErrInvitationUnavailable
	}
	target, err := s.store.FindUserByID(ctx, targetUserID)
	if err != nil {
		return InvitationRequest{}, err
	}
	if target.Disabled || strings.TrimSpace(target.Email) == "" {
		return InvitationRequest{}, ErrInvitationUnavailable
	}
	rawToken, expiresAt, err := s.issueInvitationToken(ctx, target.ID)
	if err != nil {
		return InvitationRequest{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.invitation_resent", TargetType: "user", TargetID: target.ID})
	return InvitationRequest{User: target, Token: rawToken, ExpiresAt: expiresAt}, nil
}
