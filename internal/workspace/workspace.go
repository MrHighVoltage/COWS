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
	"github.com/cows-project/cows/internal/runtime"
)

var (
	ErrInvalidWorkspace       = errors.New("invalid workspace")
	ErrTemplateNotAvailable   = errors.New("workspace template is not available")
	ErrWorkspaceNotAuthorized = errors.New("workspace access is not authorized")
	ErrRuntimeUnavailable     = errors.New("workspace runtime unavailable")
	ErrWorkspaceStateConflict = errors.New("workspace state conflict")
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
		ID:                              id,
		OwnerUserID:                     user.ID,
		TemplateID:                      template.ID,
		Name:                            name,
		DesiredState:                    domain.DesiredWorkspaceStopped,
		ObservedState:                   "unknown",
		AllocatedCPUMillis:              template.DefaultCPUMillis,
		AllocatedMemoryBytes:            template.DefaultMemoryBytes,
		AllocatedStorageBytes:           template.DefaultStorageBytes,
		InitialConnectionTimeoutSeconds: template.InitialConnectionTimeoutSeconds,
		StoppedRetentionSeconds:         template.StoppedRetentionSeconds,
		DataRetentionSeconds:            template.DataRetentionSeconds,
		CreatedAt:                       now,
		UpdatedAt:                       now,
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

func (s *Service) StartWorkspace(ctx context.Context, actorID, workspaceID string) error {
	if s.runtime == nil {
		return ErrRuntimeUnavailable
	}
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	if err := s.store.SetWorkspaceDesiredState(ctx, value.ID, domain.DesiredWorkspaceRunning, s.now().UTC()); err != nil {
		return err
	}
	if value.ObservedState == string(runtime.StateRunning) {
		return nil
	}
	if value.RuntimeID == "" || value.ObservedState == string(runtime.StateRemoved) {
		spec, err := s.runtimeSpec(ctx, value)
		if err != nil {
			return err
		}
		handle, err := s.runtime.CreateWorkspace(ctx, spec)
		if err != nil {
			_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateFailed), "", err.Error(), s.now().UTC(), s.now().UTC())
			return err
		}
		value.RuntimeID = handle.RuntimeID
		if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateCreated), handle.RuntimeID, "", s.now().UTC(), s.now().UTC()); err != nil {
			return err
		}
	}
	if err := s.runtime.StartWorkspace(ctx, value.RuntimeID); err != nil {
		_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateCreated), value.RuntimeID, err.Error(), s.now().UTC(), s.now().UTC())
		return err
	}
	now := s.now().UTC()
	value.StartedAt = now
	value.StoppedAt = time.Time{}
	if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateRunning), value.RuntimeID, "", now, now); err != nil {
		return err
	}
	if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "workspace.started", TargetType: "workspace", TargetID: value.ID})
	return nil
}

func (s *Service) StopWorkspace(ctx context.Context, actorID, workspaceID string) error {
	if s.runtime == nil {
		return ErrRuntimeUnavailable
	}
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	if err := s.store.SetWorkspaceDesiredState(ctx, value.ID, domain.DesiredWorkspaceStopped, s.now().UTC()); err != nil {
		return err
	}
	if value.RuntimeID == "" || value.ObservedState == string(runtime.StateStopped) || value.ObservedState == string(runtime.StateExited) {
		return nil
	}
	if err := s.runtime.StopWorkspace(ctx, value.RuntimeID, 10*time.Second); err != nil {
		return err
	}
	now := s.now().UTC()
	value.StoppedAt = now
	if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateStopped), value.RuntimeID, "", now, now); err != nil {
		return err
	}
	if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "workspace.stopped", TargetType: "workspace", TargetID: value.ID})
	return nil
}

func (s *Service) RestartWorkspace(ctx context.Context, actorID, workspaceID string) error {
	if err := s.StopWorkspace(ctx, actorID, workspaceID); err != nil {
		return err
	}
	return s.StartWorkspace(ctx, actorID, workspaceID)
}

func (s *Service) DeleteWorkspace(ctx context.Context, actorID, workspaceID string) error {
	if s.runtime == nil {
		return ErrRuntimeUnavailable
	}
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	if value.RuntimeID == "" {
		return nil
	}
	if value.ObservedState == string(runtime.StateRemoved) {
		return nil
	}
	if value.ObservedState == string(runtime.StateRunning) {
		return ErrWorkspaceStateConflict
	}
	if err := s.runtime.RemoveWorkspace(ctx, value.RuntimeID); err != nil {
		return err
	}
	now := s.now().UTC()
	value.ContainerDeletedAt = now
	if value.DataRetentionSeconds > 0 {
		value.DataArchiveEligibleAt = now.Add(time.Duration(value.DataRetentionSeconds) * time.Second)
	}
	if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateRemoved), value.RuntimeID, "", now, now); err != nil {
		return err
	}
	if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "workspace.container_deleted", TargetType: "workspace", TargetID: value.ID})
	return nil
}

// RecordWorkspaceConnection is the hook used by future terminal, desktop, and
// application gateways to cancel the no-connection timeout.
func (s *Service) RecordWorkspaceConnection(ctx context.Context, actorID, workspaceID string) error {
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	if value.ObservedState != string(runtime.StateRunning) {
		return ErrWorkspaceStateConflict
	}
	now := s.now().UTC()
	if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, now, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
		return err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "workspace.connected", TargetType: "workspace", TargetID: value.ID})
	return nil
}

func (s *Service) RunTimeouts(ctx context.Context) error {
	if s.runtime == nil {
		return ErrRuntimeUnavailable
	}
	values, err := s.store.ListAllWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, value := range values {
		status := EvaluateTimeouts(value, now)
		switch status.Action {
		case TimeoutActionStop:
			if value.RuntimeID == "" || value.ObservedState != string(runtime.StateRunning) {
				continue
			}
			if err := s.runtime.StopWorkspace(ctx, value.RuntimeID, 10*time.Second); err != nil {
				continue
			}
			_ = s.store.SetWorkspaceDesiredState(ctx, value.ID, domain.DesiredWorkspaceStopped, now)
			value.StoppedAt = now
			_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateStopped), value.RuntimeID, "", now, now)
			_ = s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now)
			s.recordAudit(ctx, domain.AuditEvent{EventType: "workspace.timeout_stopped", TargetType: "workspace", TargetID: value.ID})
		case TimeoutActionDelete:
			if value.RuntimeID == "" || (value.ObservedState != string(runtime.StateStopped) && value.ObservedState != string(runtime.StateExited)) {
				continue
			}
			if err := s.runtime.RemoveWorkspace(ctx, value.RuntimeID); err != nil {
				continue
			}
			value.ContainerDeletedAt = now
			if value.DataRetentionSeconds > 0 {
				value.DataArchiveEligibleAt = now.Add(time.Duration(value.DataRetentionSeconds) * time.Second)
			}
			_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateRemoved), value.RuntimeID, "", now, now)
			_ = s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now)
			s.recordAudit(ctx, domain.AuditEvent{EventType: "workspace.timeout_container_deleted", TargetType: "workspace", TargetID: value.ID})
		}
	}
	return nil
}

// Reconcile persists runtime truth without changing desired state. A missing
// container is recorded as missing, not deleted, because the absence may be a
// transient runtime or permission failure.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.runtime == nil {
		return ErrRuntimeUnavailable
	}
	inspection, err := runtime.Inspect(ctx, s.runtime)
	if err != nil {
		return err
	}
	observedByWorkspace := make(map[string]runtime.ObservedWorkspace, len(inspection.Workspaces))
	for _, observed := range inspection.Workspaces {
		observedByWorkspace[observed.WorkspaceID] = observed
	}
	values, err := s.store.ListAllWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, value := range values {
		observed, ok := observedByWorkspace[value.ID]
		if !ok {
			if value.RuntimeID == "" || value.ObservedState == string(runtime.StateRemoved) {
				continue
			}
			_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, "missing", value.RuntimeID, "managed container was not found during runtime reconciliation", inspection.ObservedAt, now)
			continue
		}
		state := normalizeObservedState(observed.State)
		startedAt := value.StartedAt
		stoppedAt := value.StoppedAt
		observedAt := observed.ObservedAt
		if observedAt.IsZero() {
			observedAt = inspection.ObservedAt
		}
		if state == string(runtime.StateRunning) {
			if startedAt.IsZero() {
				startedAt = observedAt
			}
			stoppedAt = time.Time{}
		}
		if state == string(runtime.StateStopped) && stoppedAt.IsZero() {
			stoppedAt = observedAt
		}
		if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, state, observed.RuntimeID, "", observedAt, now); err != nil {
			return err
		}
		if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, startedAt, value.LastConnectedAt, stoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
			return err
		}
	}
	return nil
}

func normalizeObservedState(state runtime.State) string {
	switch state {
	case runtime.StateRunning:
		return string(runtime.StateRunning)
	case runtime.StateCreated:
		return string(runtime.StateCreated)
	case runtime.StateStopped, runtime.StateExited:
		return string(runtime.StateStopped)
	case runtime.StateRemoved:
		return string(runtime.StateRemoved)
	case runtime.StateFailed:
		return string(runtime.StateFailed)
	default:
		return string(runtime.StateUnknown)
	}
}

func (s *Service) runtimeSpec(ctx context.Context, value domain.Workspace) (runtime.WorkspaceSpec, error) {
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return runtime.WorkspaceSpec{}, err
	}
	return runtime.WorkspaceSpec{WorkspaceID: value.ID, Image: runtime.Image{Reference: template.ImageReference, Digest: template.ImageDigest}, Limits: runtime.ResourceLimits{CPUMillis: value.AllocatedCPUMillis, MemoryBytes: value.AllocatedMemoryBytes, StorageBytes: value.AllocatedStorageBytes}, Labels: runtime.ManagedLabels(value.ID)}, nil
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
