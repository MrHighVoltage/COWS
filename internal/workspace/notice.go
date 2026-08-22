package workspace

import (
	"context"
	"fmt"
	"time"

	"github.com/cows-project/cows/internal/domain"
)

// deletionNoticeMessage describes a deletion the owner did not perform. It
// names the workspace and the reason and nothing else: no runtime identifier,
// host path, volume name, or archive location ever reaches a mailbox.
func deletionNoticeMessage(workspaceName, reason string, deletedAt time.Time, storageRetained bool) (string, string) {
	body := fmt.Sprintf("Your COWS workspace %q was deleted at %s UTC (%s).",
		workspaceName, deletedAt.UTC().Format("2006-01-02 15:04"), reason)
	if storageRetained {
		body += "\n\nIts storage was retained and can be reattached to a new workspace from the storage page."
	}
	body += "\n\nThis is an advisory notification."
	return "COWS workspace deleted", body
}

// enqueueDeletionNotice queues an advisory notice for a deletion the owner did
// not perform. It is best-effort by design: the deletion has already happened
// and cannot be undone because mail could not be queued, so the error is
// returned only for the caller to log.
func (s *Service) enqueueDeletionNotice(ctx context.Context, value domain.Workspace, reason string, storageRetained bool) error {
	owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
	if err != nil || owner.Disabled || owner.Email == "" {
		return nil
	}
	now := s.now().UTC()
	subject, body := deletionNoticeMessage(value.Name, reason, now, storageRetained)
	return s.store.UpsertEmailMessage(ctx, domain.EmailMessage{
		Kind:          domain.EmailKindWorkspaceDeleted,
		UserID:        value.OwnerUserID,
		Recipient:     owner.Email,
		Subject:       subject,
		Body:          body,
		DedupeKey:     "workspace:" + value.ID + ":" + domain.EmailKindWorkspaceDeleted,
		Status:        "pending",
		NextAttemptAt: now,
		CreatedAt:     now,
	})
}
