package notifications

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository/sqlite"
	"github.com/cows-project/cows/internal/runtime"
)

type fakeSender struct {
	error error
	sent  []domain.EmailNotification
}

func (s *fakeSender) Send(_ context.Context, notification domain.EmailNotification) error {
	if s.error != nil {
		return s.error
	}
	s.sent = append(s.sent, notification)
	return nil
}

func notificationTestStore(t *testing.T, workspace domain.Workspace) (*sqlite.Store, time.Time) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.New(db)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	if err := store.CreateUser(context.Background(), domain.User{ID: "owner-1", Username: "owner", Email: "owner@example.test", DisplayName: "Owner", Role: domain.RoleUser, CreatedAt: now, UpdatedAt: now}, "hash"); err != nil {
		t.Fatalf("create notification owner: %v", err)
	}
	if err := store.CreateTemplate(context.Background(), domain.WorkspaceTemplate{ID: "template-1", Name: "Template", ImageReference: "example/image:1", DefaultCPUMillis: 1000, MaxCPUMillis: 2000, DefaultMemoryBytes: 1 << 30, MaxMemoryBytes: 2 << 30, DefaultStorageBytes: 1 << 30, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create notification template: %v", err)
	}
	if err := store.CreateWorkspace(context.Background(), workspace); err != nil {
		t.Fatalf("create notification workspace: %v", err)
	}
	return store, now
}

func TestTimeoutWarningsAreDeduplicatedAndDelivered(t *testing.T) {
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	store, _ := notificationTestStore(t, domain.Workspace{ID: "workspace-1", OwnerUserID: "owner-1", TemplateID: "template-1", Name: "Research workspace", DesiredState: domain.DesiredWorkspaceRunning, ObservedState: string(runtime.StateRunning), RuntimeID: "runtime-1", InitialConnectionTimeoutSeconds: 3600, StartedAt: base, CreatedAt: base, UpdatedAt: base})
	sender := &fakeSender{}
	service, err := New(store, sender, time.Hour, time.Minute)
	if err != nil {
		t.Fatalf("create notification service: %v", err)
	}
	service.now = func() time.Time { return base.Add(30 * time.Minute) }
	if err := service.EnqueueTimeoutWarnings(context.Background()); err != nil {
		t.Fatalf("enqueue warning: %v", err)
	}
	if err := service.EnqueueTimeoutWarnings(context.Background()); err != nil {
		t.Fatalf("enqueue duplicate warning: %v", err)
	}
	if err := service.Deliver(context.Background()); err != nil {
		t.Fatalf("deliver warning: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].Kind != domain.NotificationTimeoutStop || sender.sent[0].Deadline != base.Add(time.Hour) {
		t.Fatalf("sent notifications = %+v", sender.sent)
	}
	if err := service.EnqueueTimeoutWarnings(context.Background()); err != nil {
		t.Fatalf("enqueue already delivered warning: %v", err)
	}
	if err := service.Deliver(context.Background()); err != nil {
		t.Fatalf("deliver already delivered warning: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("deduplicated sent notifications = %d, want 1", len(sender.sent))
	}
}

func TestFailedEmailDeliveryStopsAfterBoundedAttempts(t *testing.T) {
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	store, _ := notificationTestStore(t, domain.Workspace{ID: "workspace-1", OwnerUserID: "owner-1", TemplateID: "template-1", Name: "Research workspace", DesiredState: domain.DesiredWorkspaceRunning, ObservedState: string(runtime.StateRunning), RuntimeID: "runtime-1", InitialConnectionTimeoutSeconds: 3600, StartedAt: base, CreatedAt: base, UpdatedAt: base})
	sender := &fakeSender{error: errors.New("relay unavailable")}
	service, err := New(store, sender, time.Hour, time.Minute)
	if err != nil {
		t.Fatalf("create notification service: %v", err)
	}
	service.now = func() time.Time { return base.Add(30 * time.Minute) }
	if err := service.EnqueueTimeoutWarnings(context.Background()); err != nil {
		t.Fatalf("enqueue warning: %v", err)
	}
	for attempt := 1; attempt <= maxDeliveryAttempts; attempt++ {
		service.now = func() time.Time { return base.Add(30*time.Minute + time.Duration(attempt)*time.Minute) }
		if err := service.Deliver(context.Background()); !errors.Is(err, ErrDeliveryFailed) {
			t.Fatalf("delivery attempt %d error = %v", attempt, err)
		}
	}
	pending, err := store.ListPendingEmailNotifications(context.Background(), base.Add(2*time.Hour), 20)
	if err != nil {
		t.Fatalf("list exhausted notifications: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("exhausted notification remained pending: %+v", pending)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("failed sender recorded successful messages: %d", len(sender.sent))
	}
}
