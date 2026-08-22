package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/repository/sqlite"
)

// invitationTestService returns a service with a bootstrapped administrator
// whose forced password change is already resolved, since requireAdministrator
// refuses an actor who still owes one.
func invitationTestService(t *testing.T) (*Service, *sqlite.Store, string) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.New(db)
	service, err := New(store, time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	ctx := context.Background()
	if _, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", DisplayName: "Administrator", Password: "correct horse battery staple", Role: domain.RoleAdministrator}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, _, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := service.ChangePassword(ctx, admin.ID, "correct horse battery staple", "changed administrator password"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	return service, store, admin.ID
}

func TestBlankPasswordCreatesAnUnusableAccountAndAnInvitation(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	user, invitation, err := service.CreateUserWithInvitation(ctx, adminID, CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	if invitation.Token == "" {
		t.Fatal("expected an invitation token")
	}
	if !user.MustChangePassword {
		t.Fatal("an invited user must be forced to choose a password")
	}
	// No password may authenticate against the placeholder hash, including the
	// invitation token itself.
	for _, attempt := range []string{"", "password", "invitee", invitation.Token} {
		if _, _, err := service.Authenticate(ctx, "invitee", attempt); err == nil {
			t.Fatalf("password %q authenticated against an invited account", attempt)
		}
	}
}

func TestBlankPasswordIsRejectedWithoutADeliverableAddress(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	if _, _, err := service.CreateUserWithInvitation(ctx, adminID, CreateUserInput{
		Username: "invitee", DisplayName: "Invitee", Role: domain.RoleUser,
	}); !errors.Is(err, ErrInvitationUnavailable) {
		t.Fatalf("expected ErrInvitationUnavailable for a missing address, got %v", err)
	}

	service.SetInvitationPolicy(false, 24*time.Hour)
	if _, _, err := service.CreateUserWithInvitation(ctx, adminID, CreateUserInput{
		Username: "invitee2", Email: "invitee2@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	}); !errors.Is(err, ErrInvitationUnavailable) {
		t.Fatalf("expected ErrInvitationUnavailable when email is disabled, got %v", err)
	}
}

func TestAcceptingAnInvitationSetsThePasswordOnce(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	_, invitation, err := service.CreateUserWithInvitation(ctx, adminID, CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	accepted, err := service.AcceptInvitation(ctx, invitation.Token, "chosen-password-1", "chosen-password-1")
	if err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	if accepted.MustChangePassword {
		t.Fatal("accepting an invitation must clear the forced password change")
	}
	if _, _, err := service.Authenticate(ctx, "invitee", "chosen-password-1"); err != nil {
		t.Fatalf("authenticate after accepting: %v", err)
	}
	if _, err := service.AcceptInvitation(ctx, invitation.Token, "another-password-1", "another-password-1"); err == nil {
		t.Fatal("an invitation token must be single-use")
	}
}

func TestExpiredInvitationIsRefused(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	service.SetInvitationPolicy(true, time.Hour)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }

	_, invitation, err := service.CreateUserWithInvitation(ctx, adminID, CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	service.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := service.AcceptInvitation(ctx, invitation.Token, "chosen-password-1", "chosen-password-1"); err == nil {
		t.Fatal("an expired invitation must be refused")
	}
}

func TestATokenCannotBeSpentOnTheOtherPurpose(t *testing.T) {
	ctx := context.Background()
	service, store, adminID := invitationTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	_, invitation, err := service.CreateUserWithInvitation(ctx, adminID, CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	// An invitation offered to the reset path must be refused, and the reverse.
	if _, err := store.ResetPasswordUsingToken(ctx, hashToken(invitation.Token), domain.TokenPurposeReset, "hash", service.now().UTC()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected the reset path to refuse an invitation token, got %v", err)
	}
	request, err := service.RequestPasswordReset(ctx, "invitee")
	if err != nil {
		t.Fatalf("request reset: %v", err)
	}
	if request.Token == "" {
		t.Fatal("expected a reset token")
	}
	if _, err := service.AcceptInvitation(ctx, request.Token, "chosen-password-1", "chosen-password-1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected the invitation path to refuse a reset token, got %v", err)
	}
	// Issuing the reset must not have voided the invitation.
	if _, err := service.AcceptInvitation(ctx, invitation.Token, "chosen-password-1", "chosen-password-1"); err != nil {
		t.Fatalf("a reset in flight must not void the invitation: %v", err)
	}
}

func TestResendInvitationSupersedesThePreviousLink(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	user, first, err := service.CreateUserWithInvitation(ctx, adminID, CreateUserInput{
		Username: "invitee", Email: "invitee@example.test", DisplayName: "Invitee", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user with invitation: %v", err)
	}
	second, err := service.ResendInvitation(ctx, adminID, user.ID)
	if err != nil {
		t.Fatalf("resend invitation: %v", err)
	}
	if second.Token == first.Token {
		t.Fatal("a resent invitation must be a new token")
	}
	if _, err := service.AcceptInvitation(ctx, first.Token, "chosen-password-1", "chosen-password-1"); err == nil {
		t.Fatal("the superseded link must stop working")
	}
	if _, err := service.AcceptInvitation(ctx, second.Token, "chosen-password-1", "chosen-password-1"); err != nil {
		t.Fatalf("accept the resent invitation: %v", err)
	}
}

func TestResetRequestsAreThrottledWithoutRevealingIt(t *testing.T) {
	ctx := context.Background()
	service, store, adminID := invitationTestService(t)
	service.SetPasswordResetPolicy(2*time.Hour, 5*time.Minute)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }

	user, err := service.CreateUser(ctx, adminID, CreateUserInput{
		Username: "member", Email: "member@example.test", DisplayName: "Member",
		Password: "initial-password-1", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind: domain.EmailKindPasswordReset, UserID: user.ID, Recipient: user.Email,
		Subject: "reset", Body: "link", Status: "pending", NextAttemptAt: base, CreatedAt: base,
	}); err != nil {
		t.Fatalf("queue prior reset: %v", err)
	}

	service.now = func() time.Time { return base.Add(time.Minute) }
	request, err := service.RequestPasswordReset(ctx, "member")
	if err != nil {
		t.Fatalf("a throttled request must not error: %v", err)
	}
	if request.Token != "" {
		t.Fatal("a throttled request must not issue a token")
	}

	service.now = func() time.Time { return base.Add(10 * time.Minute) }
	if request, err = service.RequestPasswordReset(ctx, "member"); err != nil {
		t.Fatalf("request after the interval: %v", err)
	}
	if request.Token == "" {
		t.Fatal("expected a token once the interval has passed")
	}
}

func TestAdministratorTriggeredResetIssuesATokenForAnyRole(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	user, err := service.CreateUser(ctx, adminID, CreateUserInput{
		Username: "member", Email: "member@example.test", DisplayName: "Member",
		Password: "initial-password-1", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	request, err := service.SendPasswordResetFor(ctx, adminID, user.ID)
	if err != nil {
		t.Fatalf("administrator reset: %v", err)
	}
	if request.Token == "" {
		t.Fatal("expected a reset token")
	}
	if _, err := service.SendPasswordResetFor(ctx, user.ID, user.ID); err == nil {
		t.Fatal("a non-administrator must not trigger a reset for anyone")
	}
}

func TestAdministratorTriggeredResetRefusesAnAddresslessAccount(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	user, err := service.CreateUser(ctx, adminID, CreateUserInput{
		Username: "member", DisplayName: "Member",
		Password: "initial-password-1", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := service.SendPasswordResetFor(ctx, adminID, user.ID); !errors.Is(err, ErrRecoveryTargetInvalid) {
		t.Fatalf("an account without an address cannot be mailed a reset, got %v", err)
	}
}
