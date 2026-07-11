package workspace

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
)

var (
	ErrInvalidWorkspace       = errors.New("invalid workspace")
	ErrTemplateNotAvailable   = errors.New("workspace template is not available")
	ErrWorkspaceNotAuthorized = errors.New("workspace access is not authorized")
)

type CreateWorkspaceInput struct {
	Name       string
	TemplateID string
}

// Scheduler is optional for persistence-only tests and is enabled by the
// application once quota and host-capacity configuration is available.

func (s *Service) ListAvailableTemplates(ctx context.Context, actorID string) ([]domain.WorkspaceTemplate, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return nil, err
	}
	templates, err := s.store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	available := make([]domain.WorkspaceTemplate, 0, len(templates))
	for _, template := range templates {
		if template.Enabled && roleAllowed(template.AllowedRoles, user.Role) {
			available = append(available, template)
		}
	}
	return available, nil
}

func (s *Service) CreateWorkspace(ctx context.Context, actorID string, input CreateWorkspaceInput) (domain.Workspace, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return domain.Workspace{}, err
	}
	name := strings.TrimSpace(input.Name)
	if !validWorkspaceName(name) || strings.TrimSpace(input.TemplateID) == "" {
		return domain.Workspace{}, ErrInvalidWorkspace
	}
	template, err := s.store.FindTemplateByID(ctx, input.TemplateID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if !template.Enabled || !roleAllowed(template.AllowedRoles, user.Role) {
		return domain.Workspace{}, ErrTemplateNotAvailable
	}
	if s.scheduler != nil {
		if err := s.scheduler.CheckCreate(ctx, user.ID, domain.ResourceRequest{CPUMillis: template.DefaultCPUMillis, MemoryBytes: template.DefaultMemoryBytes, StorageBytes: template.DefaultStorageBytes}); err != nil {
			return domain.Workspace{}, err
		}
	}
	if _, err := s.store.FindWorkspaceByOwnerAndName(ctx, user.ID, name); err == nil {
		return domain.Workspace{}, repository.ErrConflict
	} else if !errors.Is(err, repository.ErrNotFound) {
		return domain.Workspace{}, err
	}
	id, err := newWorkspaceID()
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("create workspace ID: %w", err)
	}
	now := s.now().UTC()
	workspace := domain.Workspace{
		ID:                    id,
		OwnerUserID:           user.ID,
		TemplateID:            template.ID,
		Name:                  name,
		DesiredState:          domain.DesiredWorkspaceStopped,
		ObservedState:         "unknown",
		AllocatedCPUMillis:    template.DefaultCPUMillis,
		AllocatedMemoryBytes:  template.DefaultMemoryBytes,
		AllocatedStorageBytes: template.DefaultStorageBytes,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := s.store.CreateWorkspace(ctx, workspace); err != nil {
		return domain.Workspace{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: user.ID, EventType: "workspace.created", TargetType: "workspace", TargetID: id, Metadata: map[string]string{"template_id": template.ID}})
	return workspace, nil
}

func (s *Service) ListWorkspaces(ctx context.Context, actorID string) ([]domain.Workspace, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if user.IsAdministrator() {
		return s.store.ListAllWorkspaces(ctx)
	}
	return s.store.ListWorkspacesForUser(ctx, user.ID)
}

func (s *Service) GetWorkspace(ctx context.Context, actorID, workspaceID string) (domain.Workspace, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return domain.Workspace{}, err
	}
	workspace, err := s.store.FindWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if !user.IsAdministrator() && workspace.OwnerUserID != user.ID {
		return domain.Workspace{}, repository.ErrNotFound
	}
	return workspace, nil
}

func (s *Service) SetDesiredState(ctx context.Context, actorID, workspaceID string, state domain.DesiredWorkspaceState) error {
	workspace, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	if state != domain.DesiredWorkspaceStopped && state != domain.DesiredWorkspaceRunning {
		return ErrInvalidWorkspace
	}
	if err := s.store.SetWorkspaceDesiredState(ctx, workspace.ID, state, s.now().UTC()); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "workspace.desired_state_changed", TargetType: "workspace", TargetID: workspace.ID, Metadata: map[string]string{"state": string(state)}})
	return nil
}

// UpdateObservedState is called by reconciliation code, not by browser
// handlers. It keeps runtime truth separate from the desired state.
func (s *Service) UpdateObservedState(ctx context.Context, workspaceID, observedState, runtimeID, observedError string, observedAt time.Time) error {
	return s.store.UpdateWorkspaceObservedState(ctx, workspaceID, observedState, runtimeID, observedError, observedAt, s.now().UTC())
}

func (s *Service) requireActor(ctx context.Context, actorID string) (domain.User, error) {
	user, err := s.store.FindUserByID(ctx, actorID)
	if err != nil {
		return domain.User{}, err
	}
	if user.Disabled {
		return domain.User{}, ErrWorkspaceNotAuthorized
	}
	if user.MustChangePassword {
		return domain.User{}, ErrPasswordChangeRequired
	}
	return user, nil
}

func roleAllowed(roles []domain.Role, role domain.Role) bool {
	for _, allowed := range roles {
		if allowed == role {
			return true
		}
	}
	return false
}

func validWorkspaceName(value string) bool {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 100 {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func newWorkspaceID() (string, error) {
	bytes := make([]byte, 18)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
