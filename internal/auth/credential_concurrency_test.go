package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
)

func credentialTestUser(t *testing.T, service *Service, admin domain.User) domain.User {
	t.Helper()
	user, err := service.CreateUser(context.Background(), admin.ID, CreateUserInput{
		Username: "target", Email: "old@example.test", Password: "original target password", Role: domain.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestCSVAddressCorrectionRetiresBothCredentialPurposesAndQueuedMail(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	admin := testAdministrator(t, service)
	user := credentialTestUser(t, service, admin)
	var tokens []string
	for _, purpose := range []string{domain.TokenPurposeReset, domain.TokenPurposeInvitation} {
		token, _, err := service.issueToken(ctx, user, purpose, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
		kind := domain.EmailKindPasswordReset
		if purpose == domain.TokenPurposeInvitation {
			kind = domain.EmailKindInvitation
		}
		now := service.now().UTC()
		if err := service.store.UpsertEmailMessage(ctx, domain.EmailMessage{
			Kind: kind, UserID: user.ID, Recipient: user.Email, CredentialTokenHash: hashToken(token),
			Subject: "credential", Body: "link", NextAttemptAt: now, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ImportUsers(ctx, admin.ID, []ImportUserInput{{Username: user.Username, Email: "new@example.test"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(ctx, tokens[0], "password from former mailbox"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("old reset redemption = %v", err)
	}
	if _, err := service.AcceptInvitation(ctx, tokens[1], "password from former mailbox", "password from former mailbox"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("old invitation redemption = %v", err)
	}
	pending, err := service.store.ListPendingEmailMessages(ctx, service.now().Add(time.Minute), 50)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending credential mail = %+v, %v", pending, err)
	}
}

type pausedSessionStore struct {
	repository.Store
	entered, resume chan struct{}
}

func (s *pausedSessionStore) CreateSession(ctx context.Context, session domain.Session) error {
	close(s.entered)
	<-s.resume
	return s.Store.CreateSession(ctx, session)
}

func TestOldPasswordLoginCannotCreateSessionAfterReset(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	admin := testAdministrator(t, service)
	user := credentialTestUser(t, service, admin)
	reset, err := service.RequestPasswordReset(ctx, user.Username)
	if err != nil {
		t.Fatal(err)
	}
	paused := &pausedSessionStore{Store: service.store, entered: make(chan struct{}), resume: make(chan struct{})}
	service.store = paused
	result := make(chan error, 1)
	go func() {
		_, _, err := service.Authenticate(ctx, user.Username, "original target password")
		result <- err
	}()
	<-paused.entered
	if err := service.ResetPassword(ctx, reset.Token, "replacement target password"); err != nil {
		t.Fatal(err)
	}
	close(paused.resume)
	if err := <-result; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("late login = %v, want invalid credentials", err)
	}
	service.store = paused.Store
	if _, _, err := service.Authenticate(ctx, user.Username, "replacement target password"); err != nil {
		t.Fatalf("new password login: %v", err)
	}
}

type pausedTokenStore struct {
	repository.Store
	entered, resume chan struct{}
}

func (s *pausedTokenStore) CreatePasswordResetToken(ctx context.Context, token domain.PasswordResetToken) error {
	close(s.entered)
	<-s.resume
	return s.Store.CreatePasswordResetToken(ctx, token)
}

func TestResetIssuanceRefusesAnEarlierAccountGeneration(t *testing.T) {
	for _, mutation := range []string{"address", "address changed back", "password", "disable and enable"} {
		t.Run(mutation, func(t *testing.T) {
			service := testService(t)
			ctx := context.Background()
			admin := testAdministrator(t, service)
			user := credentialTestUser(t, service, admin)
			paused := &pausedTokenStore{Store: service.store, entered: make(chan struct{}), resume: make(chan struct{})}
			service.store = paused
			type result struct {
				request PasswordResetRequest
				err     error
			}
			done := make(chan result, 1)
			go func() {
				request, err := service.RequestPasswordReset(ctx, user.Username)
				done <- result{request, err}
			}()
			<-paused.entered
			var err error
			switch mutation {
			case "address", "address changed back":
				_, err = service.UpdateUserProfile(ctx, admin.ID, user.ID, "new@example.test", "")
				if err == nil && mutation == "address changed back" {
					_, err = service.UpdateUserProfile(ctx, admin.ID, user.ID, user.Email, "")
				}
			case "password":
				err = service.SetUserPassword(ctx, admin.ID, user.ID, "administrator replacement password")
			case "disable and enable":
				err = service.SetUserDisabled(ctx, admin.ID, user.ID, true)
				if err == nil {
					err = service.SetUserDisabled(ctx, admin.ID, user.ID, false)
				}
			}
			close(paused.resume)
			if err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil || got.request.Token != "" || got.request.User.ID != "" {
				t.Fatalf("stale reset request = %+v, %v; want non-enumerating empty result", got.request, got.err)
			}
		})
	}
}

type registrationBarrierStore struct {
	repository.Store
	entered chan struct{}
	resume  chan struct{}
}

func (s *registrationBarrierStore) RegisterUser(ctx context.Context, user domain.User, hash string, groups []string, quota domain.UserQuota) error {
	s.entered <- struct{}{}
	<-s.resume
	return s.Store.RegisterUser(ctx, user, hash, groups, quota)
}

func TestConcurrentRegistrationEnforcesEmailUniqueness(t *testing.T) {
	for _, addresses := range []struct {
		name   string
		emails []string
	}{
		{"ASCII", []string{"shared@example.test", "SHARED@example.test"}},
		{"Unicode", []string{"person@Ä.example", "person@ä.example"}},
	} {
		t.Run(addresses.name, func(t *testing.T) {
			service := testService(t)
			service.registration.Enabled = true
			barrier := &registrationBarrierStore{Store: service.store, entered: make(chan struct{}, 2), resume: make(chan struct{})}
			service.store = barrier
			done := make(chan error, 2)
			for index, email := range addresses.emails {
				go func(index int, email string) {
					_, err := service.Register(context.Background(), RegisterUserInput{
						Username: fmt.Sprintf("registrant-%d", index), Email: email,
						Password: "a registration password", PasswordConfirmation: "a registration password",
					})
					done <- err
				}(index, email)
			}
			<-barrier.entered
			<-barrier.entered
			close(barrier.resume)
			successes, conflicts := 0, 0
			for range 2 {
				err := <-done
				if err == nil {
					successes++
				} else if errors.Is(err, ErrEmailInUse) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatalf("registrations: %d succeeded, %d email conflicts", successes, conflicts)
			}

		})
	}
}

type tokenBarrierStore struct {
	repository.Store
	entered chan struct{}
	resume  chan struct{}
}

func (s *tokenBarrierStore) CreatePasswordResetToken(ctx context.Context, token domain.PasswordResetToken) error {
	s.entered <- struct{}{}
	<-s.resume
	return s.Store.CreatePasswordResetToken(ctx, token)
}

func TestConcurrentResetRequestsRespectAccountThrottleAndPurpose(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	admin := testAdministrator(t, service)
	user := credentialTestUser(t, service, admin)
	invitation, _, err := service.issueToken(ctx, user, domain.TokenPurposeInvitation, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const count = 8
	barrier := &tokenBarrierStore{Store: service.store, entered: make(chan struct{}, count), resume: make(chan struct{})}
	service.store = barrier
	var wg sync.WaitGroup
	results := make(chan PasswordResetRequest, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, err := service.RequestPasswordReset(ctx, user.Username)
			if err != nil {
				t.Errorf("request reset: %v", err)
			}
			results <- request
		}()
	}
	for range count {
		<-barrier.entered
	}
	close(barrier.resume)
	wg.Wait()
	close(results)
	issued := 0
	for request := range results {
		if request.Token != "" {
			issued++
		}
	}
	if issued != 1 {
		t.Fatalf("concurrent reset tokens issued = %d, want 1", issued)
	}
	if _, err := service.AcceptInvitation(ctx, invitation, "an invitation password", "an invitation password"); err != nil {
		t.Fatalf("reset issuance retired a different-purpose token: %v", err)
	}
}
