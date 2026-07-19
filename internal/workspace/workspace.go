package workspace

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/quota"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
)

var (
	ErrInvalidWorkspace            = errors.New("invalid workspace")
	ErrTemplateNotAvailable        = errors.New("workspace template is not available")
	ErrWorkspaceNotAuthorized      = errors.New("workspace access is not authorized")
	ErrRuntimeUnavailable          = errors.New("workspace runtime unavailable")
	ErrWorkspaceStateConflict      = errors.New("workspace state conflict")
	ErrTerminalNotAvailable        = errors.New("workspace terminal is not available")
	ErrTerminalIdentityNotAllowed  = errors.New("workspace terminal identity is not allowed")
	ErrDesktopNotAvailable         = errors.New("workspace desktop is not available")
	ErrFileManagerNotAvailable     = errors.New("workspace file manager is not available")
	ErrWorkspaceCleanupIncomplete  = errors.New("workspace cleanup incomplete")
	ErrUserCleanupRequiresDisabled = errors.New("user must be disabled before workspace cleanup")
)

type WorkspaceCleanupError struct {
	Operation string
	Failed    int
}

func (e *WorkspaceCleanupError) Error() string {
	return fmt.Sprintf("%s failed for %d workspace(s)", e.Operation, e.Failed)
}

func (e *WorkspaceCleanupError) Unwrap() error { return ErrWorkspaceCleanupIncomplete }

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
		allowed, accessErr := s.templateAllowed(ctx, user, template)
		if accessErr != nil {
			return nil, accessErr
		}
		if template.Enabled && allowed {
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
	allowed, accessErr := s.templateAllowed(ctx, user, template)
	if accessErr != nil {
		return domain.Workspace{}, accessErr
	}
	if !template.Enabled || !allowed {
		return domain.Workspace{}, ErrTemplateNotAvailable
	}
	templateSecrets, err := resolveTemplateSecrets(template.Configuration)
	if err != nil {
		return domain.Workspace{}, err
	}
	releaseAdmission := s.acquireAdmission()
	defer releaseAdmission()
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
	if err := ensureMountDirectories(s.mountRoot, id, template.Configuration.Mounts); err != nil {
		return domain.Workspace{}, err
	}
	now := s.now().UTC()
	workspace := domain.Workspace{
		ID:                              id,
		OwnerUserID:                     user.ID,
		TemplateID:                      template.ID,
		TemplateRevision:                template.Revision,
		TemplateImageReference:          template.ImageReference,
		TemplateImageDigest:             template.ImageDigest,
		TemplateConfiguration:           cloneTemplateConfiguration(template.Configuration),
		TemplateSecrets:                 templateSecrets,
		Name:                            name,
		DesiredState:                    domain.DesiredWorkspaceStopped,
		ObservedState:                   "unknown",
		AllocatedCPUMillis:              template.DefaultCPUMillis,
		AllocatedMemoryBytes:            template.DefaultMemoryBytes,
		AllocatedStorageBytes:           template.DefaultStorageBytes,
		InitialConnectionTimeoutSeconds: template.InitialConnectionTimeoutSeconds,
		StoppedRetentionSeconds:         template.StoppedRetentionSeconds,
		CreatedAt:                       now,
		UpdatedAt:                       now,
	}
	if err := s.store.CreateWorkspace(ctx, workspace); err != nil {
		_ = removeMountDirectories(s.mountRoot, id, template.Configuration.Mounts)
		return domain.Workspace{}, err
	}
	if err := s.reserveWorkspacePorts(ctx, id, template.Configuration.Services); err != nil {
		_ = s.store.DeleteWorkspace(ctx, id)
		_ = removeMountDirectories(s.mountRoot, id, template.Configuration.Mounts)
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
	values, err := s.store.ListWorkspacesForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	values, err = s.withTemplateNames(ctx, values)
	if err != nil {
		return nil, err
	}
	values, err = s.withStorageUsage(ctx, values)
	if err != nil {
		return nil, err
	}
	return s.withResourceUsage(ctx, values), nil
}

// StopUserWorkspaces reconciles runtime state and stops every currently
// running workspace owned by targetUserID. It is used after account sessions
// have been invalidated, so a failed runtime operation cannot leave the user
// with an active session.
func (s *Service) StopUserWorkspaces(ctx context.Context, actorID, targetUserID string) error {
	if err := s.requireAdministratorActor(ctx, actorID); err != nil {
		return err
	}
	if _, err := s.store.FindUserByID(ctx, targetUserID); err != nil {
		return err
	}
	if err := s.Reconcile(ctx); err != nil {
		return err
	}
	values, err := s.store.ListWorkspacesForUser(ctx, targetUserID)
	if err != nil {
		return err
	}
	failed := 0
	for _, value := range values {
		if value.RuntimeID == "" || value.ObservedState != string(runtime.StateRunning) {
			continue
		}
		if err := s.StopWorkspace(ctx, actorID, value.ID); err != nil {
			failed++
		}
	}
	if failed > 0 {
		return &WorkspaceCleanupError{Operation: "stopping user workspaces", Failed: failed}
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.workspaces_stopped", TargetType: "user", TargetID: targetUserID, Metadata: map[string]string{"workspace_count": fmt.Sprintf("%d", len(values))}})
	return nil
}

// DeleteUserWorkspaces stops any still-running workspace and then performs
// the ordinary explicit deletion path, including directory archival and named
// volume tombstones. The user record must be deleted separately afterwards.
func (s *Service) DeleteUserWorkspaces(ctx context.Context, actorID, targetUserID string) error {
	if err := s.requireAdministratorActor(ctx, actorID); err != nil {
		return err
	}
	target, err := s.store.FindUserByID(ctx, targetUserID)
	if err != nil {
		return err
	}
	if !target.Disabled {
		return ErrUserCleanupRequiresDisabled
	}
	if err := s.Reconcile(ctx); err != nil {
		return err
	}
	values, err := s.store.ListWorkspacesForUser(ctx, targetUserID)
	if err != nil {
		return err
	}
	failed := 0
	for _, value := range values {
		if value.RuntimeID != "" && value.ObservedState == string(runtime.StateRunning) {
			if err := s.StopWorkspace(ctx, actorID, value.ID); err != nil {
				failed++
				continue
			}
		}
		if err := s.DeleteWorkspace(ctx, actorID, value.ID); err != nil {
			failed++
		}
	}
	if failed > 0 {
		return &WorkspaceCleanupError{Operation: "deleting user workspaces", Failed: failed}
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "user.workspaces_deleted", TargetType: "user", TargetID: targetUserID, Metadata: map[string]string{"workspace_count": fmt.Sprintf("%d", len(values))}})
	return nil
}

func (s *Service) withTemplateNames(ctx context.Context, values []domain.Workspace) ([]domain.Workspace, error) {
	templates, err := s.store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(templates))
	for _, template := range templates {
		names[template.ID] = template.Name
	}
	for index := range values {
		name, ok := names[values[index].TemplateID]
		if !ok {
			return nil, repository.ErrNotFound
		}
		values[index].TemplateName = name
	}
	return values, nil
}

func (s *Service) WorkspaceAccessMethodsForWorkspaces(ctx context.Context, actorID string, values []domain.Workspace) (map[string][]domain.AccessMethod, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return nil, err
	}
	templates, err := s.store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	accessByTemplate := make(map[string][]domain.AccessMethod, len(templates))
	for _, template := range templates {
		accessByTemplate[template.ID] = template.AccessMethods
	}
	access := make(map[string][]domain.AccessMethod, len(values))
	for _, value := range values {
		if !user.IsAdministrator() && value.OwnerUserID != user.ID {
			return nil, repository.ErrNotFound
		}
		methods, ok := accessByTemplate[value.TemplateID]
		if !ok {
			return nil, repository.ErrNotFound
		}
		access[value.ID] = append([]domain.AccessMethod(nil), methods...)
	}
	return access, nil
}

// ListWorkspacesForRuntimeOverview returns control-plane records without
// measuring storage. The runtime page must remain useful when a helper or
// container is temporarily unavailable; admission still fails closed.
func (s *Service) ListWorkspacesForRuntimeOverview(ctx context.Context, actorID string) ([]domain.Workspace, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if user.IsAdministrator() {
		return s.store.ListAllWorkspaces(ctx)
	}
	return s.store.ListWorkspacesForUser(ctx, user.ID)
}

func (s *Service) AllocationSummary(ctx context.Context, actorID string) (domain.AllocationSummary, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return domain.AllocationSummary{}, err
	}
	values, err := s.store.ListWorkspacesForUser(ctx, user.ID)
	if err != nil {
		return domain.AllocationSummary{}, err
	}
	values, err = s.withStorageUsage(ctx, values)
	if err != nil {
		return domain.AllocationSummary{}, err
	}
	summary, known, err := s.allocationSummaryForWorkspaces(ctx, user.ID, values)
	if err != nil {
		return domain.AllocationSummary{}, err
	}
	if !known {
		return domain.AllocationSummary{}, quota.ErrStorageUnavailable
	}
	return summary, nil
}

func (s *Service) AllocationSummaryForWorkspaces(ctx context.Context, actorID string, values []domain.Workspace) (domain.AllocationSummary, bool, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return domain.AllocationSummary{}, false, err
	}
	return s.allocationSummaryForWorkspaces(ctx, user.ID, values)
}

func (s *Service) allocationSummaryForWorkspaces(ctx context.Context, userID string, values []domain.Workspace) (domain.AllocationSummary, bool, error) {
	summary, err := s.store.WorkspaceAllocations(ctx, userID)
	if err != nil {
		return domain.AllocationSummary{}, false, err
	}
	known := true
	if s.storageUsage == nil {
		return summary, true, nil
	}
	for _, value := range values {
		if !value.StorageUsageKnown {
			known = false
			continue
		}
		summary.Resources.StorageBytes += value.StorageUsageBytes
	}
	return summary, known, nil
}

func (s *Service) withStorageUsage(ctx context.Context, values []domain.Workspace) ([]domain.Workspace, error) {
	if s.storageUsage == nil {
		return values, nil
	}
	for index := range values {
		usage, err := s.storageUsage.WorkspaceStorageUsage(ctx, values[index])
		if err != nil {
			continue
		}
		values[index].StorageUsageBytes = usage
		values[index].StorageUsageKnown = true
	}
	return values, nil
}

func (s *Service) withResourceUsage(ctx context.Context, values []domain.Workspace) []domain.Workspace {
	if s.resourceUsage == nil {
		return values
	}
	for index := range values {
		value := &values[index]
		if value.ObservedState != string(runtime.StateRunning) || value.RuntimeID == "" {
			continue
		}
		usage, err := s.resourceUsage.WorkspaceResourceUsage(ctx, value.RuntimeID)
		if err != nil {
			continue
		}
		value.CPUUsagePercentMilli = usage.CPUPercentMilli
		value.MemoryUsageBytes = usage.MemoryBytes
		value.PIDsUsage = usage.PIDs
		value.ResourceUsageKnown = true
	}
	return values
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

// WorkspaceAccessMethods returns the current template access policy after
// applying the same workspace ownership checks used by workspace operations.
func (s *Service) WorkspaceAccessMethods(ctx context.Context, actorID, workspaceID string) ([]domain.AccessMethod, error) {
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return nil, err
	}
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return nil, err
	}
	return append([]domain.AccessMethod(nil), template.AccessMethods...), nil
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
	release, err := s.beginLifecycleChange(ctx, value.ID)
	if err != nil {
		return err
	}
	defer release()
	releaseAdmission := s.acquireAdmission()
	defer releaseAdmission()
	if value.ObservedState != string(runtime.StateRunning) && s.scheduler != nil {
		if err := s.scheduler.CheckStart(ctx, value.OwnerUserID, domain.ResourceRequest{CPUMillis: value.AllocatedCPUMillis, MemoryBytes: value.AllocatedMemoryBytes}); err != nil {
			return err
		}
	}
	if err := s.store.SetWorkspaceDesiredState(ctx, value.ID, domain.DesiredWorkspaceRunning, s.now().UTC()); err != nil {
		return err
	}
	operationStarted := s.now().UTC()
	if err := s.beginOperation(ctx, value.ID, "start", operationStarted); err != nil {
		return err
	}
	if value.ObservedState == string(runtime.StateRunning) {
		return s.finishOperation(ctx, value.ID, "start", "succeeded", "", operationStarted)
	}
	if value.RuntimeID == "" || value.ObservedState == string(runtime.StateRemoved) || value.ObservedState == "missing" {
		_ = s.updateOperationPhase(ctx, value.ID, "start:preparing")
		configuration, err := s.effectiveConfiguration(ctx, value)
		if err != nil {
			return err
		}
		if err := s.ensureWorkspacePorts(ctx, value.ID, configuration); err != nil {
			return err
		}
		if err := ensureMountDirectories(s.mountRoot, value.ID, configuration.Mounts); err != nil {
			return err
		}
		_ = s.updateOperationPhase(ctx, value.ID, "start:creating")
		spec, err := s.runtimeSpec(ctx, value)
		if err != nil {
			return err
		}
		handle, err := s.runtime.CreateWorkspace(ctx, spec)
		if err != nil {
			_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateFailed), "", "runtime_create_failed", err.Error(), s.now().UTC(), s.now().UTC())
			_ = s.finishOperation(ctx, value.ID, "start", "failed", err.Error(), operationStarted)
			return err
		}
		value.RuntimeID = handle.RuntimeID
		if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateCreated), handle.RuntimeID, "", "", s.now().UTC(), s.now().UTC()); err != nil {
			return err
		}
	}
	_ = s.updateOperationPhase(ctx, value.ID, "start:starting")
	if err := s.runtime.StartWorkspace(ctx, value.RuntimeID); err != nil {
		_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateCreated), value.RuntimeID, "runtime_start_failed", err.Error(), s.now().UTC(), s.now().UTC())
		_ = s.finishOperation(ctx, value.ID, "start", "failed", err.Error(), operationStarted)
		return err
	}
	now := s.now().UTC()
	value.StartedAt = now
	value.LastConnectedAt = time.Time{}
	value.StoppedAt = time.Time{}
	if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateRunning), value.RuntimeID, "", "", now, now); err != nil {
		return err
	}
	if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
		return err
	}
	if err := s.finishOperation(ctx, value.ID, "start", "succeeded", "", operationStarted); err != nil {
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
	release, err := s.beginLifecycleChange(ctx, value.ID)
	if err != nil {
		return err
	}
	defer release()
	releaseAdmission := s.acquireAdmission()
	defer releaseAdmission()
	if err := s.store.SetWorkspaceDesiredState(ctx, value.ID, domain.DesiredWorkspaceStopped, s.now().UTC()); err != nil {
		return err
	}
	operationStarted := s.now().UTC()
	if err := s.beginOperation(ctx, value.ID, "stop", operationStarted); err != nil {
		return err
	}
	_ = s.updateOperationPhase(ctx, value.ID, "stop:stopping")
	if value.RuntimeID == "" || value.ObservedState == string(runtime.StateStopped) || value.ObservedState == string(runtime.StateExited) || value.ObservedState == "missing" {
		return s.finishOperation(ctx, value.ID, "stop", "succeeded", "", operationStarted)
	}
	if err := s.runtime.StopWorkspace(ctx, value.RuntimeID, 10*time.Second); err != nil {
		_ = s.finishOperation(ctx, value.ID, "stop", "failed", err.Error(), operationStarted)
		return err
	}
	now := s.now().UTC()
	value.StoppedAt = now
	if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateStopped), value.RuntimeID, "", "", now, now); err != nil {
		return err
	}
	if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
		return err
	}
	if err := s.finishOperation(ctx, value.ID, "stop", "succeeded", "", operationStarted); err != nil {
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
	release, err := s.beginLifecycleChange(ctx, value.ID)
	if err != nil {
		return err
	}
	defer release()
	releaseAdmission := s.acquireAdmission()
	defer releaseAdmission()
	configuration, err := s.effectiveConfiguration(ctx, value)
	if err != nil {
		return err
	}
	archiveAction := archiveMountActivityAction(configuration.Mounts)
	if value.RuntimeID == "" {
		if err := s.logArchiveActivity(value, "workspace_delete_started", "started", nil); err != nil {
			return err
		}
		if err := archiveMountDirectories(s.mountRoot, s.mountArchiveRoot, value.ID, configuration.Mounts); err != nil {
			_ = s.logArchiveActivity(value, archiveAction, "failed", err)
			return err
		}
		if err := s.logArchiveActivity(value, archiveAction, "succeeded", nil); err != nil {
			return err
		}
		if err := s.store.CancelEmailNotificationsForWorkspace(ctx, value.ID); err != nil {
			return err
		}
		if err := s.store.DeleteWorkspace(ctx, value.ID); err != nil {
			return err
		}
		_, archivePath := archiveActivityPaths(s.mountRoot, s.mountArchiveRoot, value.ID)
		s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "workspace.deleted", TargetType: "workspace", TargetID: value.ID, Metadata: map[string]string{"runtime_id": value.RuntimeID, "archive_path": archivePath}})
		return nil
	}
	if value.ObservedState == string(runtime.StateRunning) {
		return ErrWorkspaceStateConflict
	}
	operationStarted := s.now().UTC()
	if err := s.beginOperation(ctx, value.ID, "delete", operationStarted); err != nil {
		return err
	}
	if err := s.logArchiveActivity(value, "workspace_delete_started", "started", nil); err != nil {
		_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
		return err
	}
	_ = s.updateOperationPhase(ctx, value.ID, "delete:removing")
	if value.ObservedState != string(runtime.StateRemoved) && value.ObservedState != "missing" {
		if err := s.runtime.RemoveWorkspace(ctx, value.RuntimeID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			_ = s.logArchiveActivity(value, "runtime_container_removed", "failed", err)
			_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
			return err
		}
		if err := s.logArchiveActivity(value, "runtime_container_removed", "succeeded", nil); err != nil {
			_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
			return err
		}
	}
	_ = s.updateOperationPhase(ctx, value.ID, "delete:archiving")
	if err := archiveMountDirectories(s.mountRoot, s.mountArchiveRoot, value.ID, configuration.Mounts); err != nil {
		_ = s.logArchiveActivity(value, archiveAction, "failed", err)
		_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
		return err
	}
	if err := s.logArchiveActivity(value, archiveAction, "succeeded", nil); err != nil {
		_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
		return err
	}
	// Keep the record when this database delete fails so an administrator can
	// retry without losing the audit and reconciliation context.
	retainedVolumes := retainedWorkspaceVolumes(value, configuration.Mounts, s.now().UTC())
	if len(retainedVolumes) > 0 {
		if err := s.logArchiveActivity(value, "named_volumes_retained", "succeeded", nil); err != nil {
			_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
			return err
		}
	}
	if err := s.store.CancelEmailNotificationsForWorkspace(ctx, value.ID); err != nil {
		_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
		return err
	}
	if err := s.store.DeleteWorkspaceRetainingVolumes(ctx, value.ID, retainedVolumes); err != nil {
		_ = s.finishOperation(ctx, value.ID, "delete", "failed", err.Error(), operationStarted)
		return err
	}
	_, archivePath := archiveActivityPaths(s.mountRoot, s.mountArchiveRoot, value.ID)
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "workspace.deleted", TargetType: "workspace", TargetID: value.ID, Metadata: map[string]string{"retained_volume_count": fmt.Sprintf("%d", len(retainedVolumes)), "runtime_id": value.RuntimeID, "archive_path": archivePath}})
	return nil
}

func retainedWorkspaceVolumes(value domain.Workspace, mounts []domain.TemplateMount, retainedAt time.Time) []domain.RetainedWorkspaceVolume {
	volumes := make([]domain.RetainedWorkspaceVolume, 0)
	for _, mount := range mounts {
		if normalizedMountType(mount.Type) != domain.TemplateMountVolume {
			continue
		}
		volumes = append(volumes, domain.RetainedWorkspaceVolume{
			VolumeName:    managedVolumeName(value.ID, mount),
			WorkspaceID:   value.ID,
			OwnerUserID:   value.OwnerUserID,
			TemplateID:    value.TemplateID,
			MountName:     mount.Name,
			ContainerPath: mount.ContainerPath,
			ReadOnly:      mount.ReadOnly,
			RetainedAt:    retainedAt,
		})
	}
	return volumes
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

// OpenTerminal authorizes the workspace and template before asking the runtime
// adapter to attach an approved shell. The browser never supplies a runtime ID
// or command.
func (s *Service) OpenTerminal(ctx context.Context, actorID, workspaceID string, requestedUID *int64) (runtime.Terminal, error) {
	if s.runtime == nil {
		return nil, ErrRuntimeUnavailable
	}
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return nil, err
	}
	if value.ObservedState != string(runtime.StateRunning) || value.RuntimeID == "" {
		return nil, ErrWorkspaceStateConflict
	}
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return nil, err
	}
	if !hasAccessMethod(template.AccessMethods, domain.AccessTerminal) {
		return nil, ErrTerminalNotAvailable
	}
	shellRuntime, ok := s.runtime.(runtime.ShellRuntime)
	if !ok {
		return nil, ErrTerminalNotAvailable
	}
	configuration, err := s.effectiveConfiguration(ctx, value)
	if err != nil {
		return nil, err
	}
	owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
	if err != nil {
		return nil, err
	}
	allocations, err := s.store.ListWorkspacePortAllocations(ctx, value.ID)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveConfiguration(configuration, owner, value.ID, value.Name, allocations, value.TemplateSecrets)
	if err != nil {
		return nil, err
	}
	shell := "/bin/sh"
	if resolved.User != nil && resolved.User.Shell != "" {
		shell = resolved.User.Shell
	}
	var terminal runtime.Terminal
	if len(configuration.TerminalUIDs) == 0 {
		if requestedUID != nil {
			return nil, ErrTerminalIdentityNotAllowed
		}
		terminal, err = shellRuntime.OpenShell(ctx, value.RuntimeID, []string{shell, "-l"})
	} else {
		uid := configuration.TerminalUIDs[0]
		if requestedUID != nil {
			uid = *requestedUID
		}
		if !containsInt64(configuration.TerminalUIDs, uid) {
			return nil, ErrTerminalIdentityNotAllowed
		}
		userShellRuntime, ok := s.runtime.(runtime.UserShellRuntime)
		if !ok {
			return nil, ErrTerminalNotAvailable
		}
		terminal, err = userShellRuntime.OpenShellAs(ctx, value.RuntimeID, uid, []string{shell, "-l"})
	}
	if err != nil {
		return nil, err
	}
	if err := s.RecordWorkspaceConnection(ctx, actorID, workspaceID); err != nil {
		_ = terminal.Close()
		return nil, err
	}
	metadata := map[string]string{}
	if len(configuration.TerminalUIDs) > 0 {
		uid := configuration.TerminalUIDs[0]
		if requestedUID != nil {
			uid = *requestedUID
		}
		metadata["uid"] = fmt.Sprintf("%d", uid)
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "terminal.session_started", TargetType: "workspace", TargetID: workspaceID, Metadata: metadata})
	return terminal, nil
}

func containsInt64(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Service) RecordTerminalDisconnect(ctx context.Context, actorID, workspaceID string) {
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "terminal.session_ended", TargetType: "workspace", TargetID: workspaceID})
}

// OpenDesktop authorizes the fixed desktop service and asks the runtime to
// connect only to the persisted loopback port allocated for that service.
func (s *Service) OpenDesktop(ctx context.Context, actorID, workspaceID string) (io.ReadWriteCloser, error) {
	if s.runtime == nil {
		return nil, ErrRuntimeUnavailable
	}
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return nil, err
	}
	if value.ObservedState != string(runtime.StateRunning) || value.RuntimeID == "" {
		return nil, ErrWorkspaceStateConflict
	}
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return nil, err
	}
	if !hasAccessMethod(template.AccessMethods, domain.AccessDesktop) {
		return nil, ErrDesktopNotAvailable
	}
	configuration, err := s.effectiveConfiguration(ctx, value)
	if err != nil {
		return nil, err
	}
	desktopService, ok := findDesktopService(configuration)
	if !ok {
		return nil, ErrDesktopNotAvailable
	}
	allocations, err := s.store.ListWorkspacePortAllocations(ctx, value.ID)
	if err != nil {
		return nil, err
	}
	for _, allocation := range allocations {
		if allocation.ServiceName != desktopService.Name || allocation.Protocol != "tcp" {
			continue
		}
		desktopRuntime, ok := s.runtime.(runtime.InternalServiceRuntime)
		if !ok {
			return nil, ErrDesktopNotAvailable
		}
		connection, err := desktopRuntime.OpenInternalService(ctx, value.RuntimeID, desktopService.ContainerPort, allocation.HostPort)
		if err != nil {
			return nil, err
		}
		if err := s.RecordWorkspaceConnection(ctx, actorID, workspaceID); err != nil {
			_ = connection.Close()
			return nil, err
		}
		s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "desktop.session_started", TargetType: "workspace", TargetID: workspaceID})
		return connection, nil
	}
	return nil, ErrDesktopNotAvailable
}

// GetDesktopCredentials returns a workspace-scoped VNC password only after
// the normal workspace authorization and desktop availability checks pass.
// The web layer uses this for noVNC's credentialsrequired event; it is not a
// general workspace secret endpoint.
func (s *Service) GetDesktopCredentials(ctx context.Context, actorID, workspaceID string) (string, error) {
	value, err := s.GetWorkspace(ctx, actorID, workspaceID)
	if err != nil {
		return "", err
	}
	if value.ObservedState != string(runtime.StateRunning) || value.RuntimeID == "" {
		return "", ErrDesktopNotAvailable
	}
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return "", err
	}
	configuration, err := s.effectiveConfiguration(ctx, value)
	if err != nil {
		return "", err
	}
	if !hasAccessMethod(template.AccessMethods, domain.AccessDesktop) {
		return "", ErrDesktopNotAvailable
	}
	desktopService, ok := findDesktopService(configuration)
	if !ok || desktopService.PasswordSecret == "" {
		return "", ErrDesktopNotAvailable
	}
	password, ok := value.TemplateSecrets[desktopService.PasswordSecret]
	if !ok || password == "" {
		return "", ErrDesktopNotAvailable
	}
	return password, nil
}

func (s *Service) RecordDesktopDisconnect(ctx context.Context, actorID, workspaceID string) {
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "desktop.session_ended", TargetType: "workspace", TargetID: workspaceID})
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
	failed := 0
	for _, value := range values {
		status := EvaluateTimeouts(value, now)
		release, lockErr := s.beginLifecycleChange(ctx, value.ID)
		if lockErr != nil {
			if errors.Is(lockErr, context.Canceled) || errors.Is(lockErr, context.DeadlineExceeded) {
				return lockErr
			}
			continue
		}
		releaseAdmission := s.acquireAdmission()
		func() {
			defer releaseAdmission()
			switch status.Action {
			case TimeoutActionStop:
				if value.RuntimeID == "" || value.ObservedState != string(runtime.StateRunning) {
					return
				}
				operationStarted := now
				if err := s.beginOperation(ctx, value.ID, "timeout-stop", operationStarted); err != nil {
					failed++
					return
				}
				if err := s.runtime.StopWorkspace(ctx, value.RuntimeID, 10*time.Second); err != nil {
					failed++
					_ = s.finishOperation(ctx, value.ID, "timeout-stop", "failed", err.Error(), operationStarted)
					return
				}
				var operationErr error
				remember := func(err error) {
					if operationErr == nil && err != nil {
						operationErr = err
					}
				}
				remember(s.store.SetWorkspaceDesiredState(ctx, value.ID, domain.DesiredWorkspaceStopped, now))
				value.StoppedAt = now
				remember(s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateStopped), value.RuntimeID, "", "", now, now))
				remember(s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now))
				if operationErr != nil {
					failed++
					_ = s.finishOperation(ctx, value.ID, "timeout-stop", "failed", operationErr.Error(), operationStarted)
					return
				}
				if err := s.finishOperation(ctx, value.ID, "timeout-stop", "succeeded", "", operationStarted); err != nil {
					failed++
					return
				}
				s.recordAudit(ctx, domain.AuditEvent{EventType: "workspace.timeout_stopped", TargetType: "workspace", TargetID: value.ID})
			case TimeoutActionDelete:
				if value.RuntimeID == "" || (value.ObservedState != string(runtime.StateStopped) && value.ObservedState != string(runtime.StateExited)) {
					return
				}
				operationStarted := now
				if err := s.beginOperation(ctx, value.ID, "timeout-delete", operationStarted); err != nil {
					failed++
					return
				}
				if err := s.runtime.RemoveWorkspace(ctx, value.RuntimeID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
					failed++
					_ = s.finishOperation(ctx, value.ID, "timeout-delete", "failed", err.Error(), operationStarted)
					return
				}
				var operationErr error
				remember := func(err error) {
					if operationErr == nil && err != nil {
						operationErr = err
					}
				}
				remember(s.store.CancelEmailNotificationsForWorkspace(ctx, value.ID))
				remember(s.store.ReleaseWorkspacePorts(ctx, value.ID))
				value.ContainerDeletedAt = now
				value.DataArchiveEligibleAt = time.Time{}
				remember(s.store.UpdateWorkspaceObservedState(ctx, value.ID, string(runtime.StateRemoved), value.RuntimeID, "", "", now, now))
				remember(s.store.UpdateWorkspaceLifecycle(ctx, value.ID, value.StartedAt, value.LastConnectedAt, value.StoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now))
				if operationErr != nil {
					failed++
					_ = s.finishOperation(ctx, value.ID, "timeout-delete", "failed", operationErr.Error(), operationStarted)
					return
				}
				if err := s.finishOperation(ctx, value.ID, "timeout-delete", "succeeded", "", operationStarted); err != nil {
					failed++
					return
				}
				s.recordAudit(ctx, domain.AuditEvent{EventType: "workspace.timeout_container_deleted", TargetType: "workspace", TargetID: value.ID})
			}
		}()
		release()
	}
	if failed > 0 {
		return &WorkspaceCleanupError{Operation: "timeout processing", Failed: failed}
	}
	return nil
}

func (s *Service) beginOperation(ctx context.Context, workspaceID, operation string, startedAt time.Time) error {
	return s.store.UpdateWorkspaceOperation(ctx, workspaceID, operation, "running", "", startedAt, startedAt)
}

func (s *Service) acquireAdmission() func() {
	s.admissionMu.Lock()
	return s.admissionMu.Unlock
}

func (s *Service) finishOperation(ctx context.Context, workspaceID, operation, status, operationError string, startedAt time.Time) error {
	return s.store.UpdateWorkspaceOperation(ctx, workspaceID, operation, status, operationError, startedAt, s.now().UTC())
}

func (s *Service) updateOperationPhase(ctx context.Context, workspaceID, phase string) error {
	return s.store.UpdateWorkspaceOperation(ctx, workspaceID, phase, "running", "", time.Time{}, s.now().UTC())
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
			_ = s.store.UpdateWorkspaceObservedState(ctx, value.ID, "missing", value.RuntimeID, "runtime_missing", "managed container was not found during runtime reconciliation", inspection.ObservedAt, now)
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
		if err := s.store.UpdateWorkspaceObservedState(ctx, value.ID, state, observed.RuntimeID, "", "", observedAt, now); err != nil {
			return err
		}
		if err := s.store.UpdateWorkspaceLifecycle(ctx, value.ID, startedAt, value.LastConnectedAt, stoppedAt, value.ContainerDeletedAt, value.DataArchiveEligibleAt, now); err != nil {
			return err
		}
	}
	knownWorkspaceIDs := make(map[string]struct{}, len(values))
	for _, value := range values {
		knownWorkspaceIDs[value.ID] = struct{}{}
	}
	for _, observed := range inspection.Workspaces {
		if _, ok := knownWorkspaceIDs[observed.WorkspaceID]; ok {
			continue
		}
		s.recordAudit(ctx, domain.AuditEvent{EventType: "runtime.orphaned_container", TargetType: "runtime", TargetID: observed.RuntimeID, Metadata: map[string]string{"workspace_id": observed.WorkspaceID, "state": string(observed.State)}})
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
	configuration, err := s.effectiveConfiguration(ctx, value)
	if err != nil {
		return runtime.WorkspaceSpec{}, err
	}
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return runtime.WorkspaceSpec{}, err
	}
	allocations, err := s.store.ListWorkspacePortAllocations(ctx, value.ID)
	if err != nil {
		return runtime.WorkspaceSpec{}, err
	}
	owner, err := s.store.FindUserByID(ctx, value.OwnerUserID)
	if err != nil {
		return runtime.WorkspaceSpec{}, err
	}
	resolved, err := resolveConfiguration(configuration, owner, value.ID, value.Name, allocations, value.TemplateSecrets)
	if err != nil {
		return runtime.WorkspaceSpec{}, err
	}
	networkMode := "none"
	if len(resolved.Ports) > 0 {
		networkMode = "bridge"
	}
	mounts := make([]runtime.Mount, 0, len(configuration.Mounts))
	for _, definition := range configuration.Mounts {
		mount, err := materializeMount(s.mountRoot, value.ID, definition)
		if err != nil {
			return runtime.WorkspaceSpec{}, err
		}
		mounts = append(mounts, mount)
	}
	image := runtime.Image{Reference: value.TemplateImageReference, Digest: value.TemplateImageDigest}
	if image.Reference == "" {
		image = runtime.Image{Reference: template.ImageReference, Digest: template.ImageDigest}
	}
	return runtime.WorkspaceSpec{WorkspaceID: value.ID, Image: image, Limits: runtime.ResourceLimits{CPUMillis: value.AllocatedCPUMillis, MemoryBytes: value.AllocatedMemoryBytes, StorageBytes: value.AllocatedStorageBytes}, Labels: runtime.ManagedLabels(value.ID), Command: resolved.Command, Environment: resolved.Environment, Mounts: mounts, Ports: resolved.Ports, NetworkMode: networkMode, User: resolved.User}, nil
}

func (s *Service) effectiveConfiguration(ctx context.Context, value domain.Workspace) (domain.TemplateConfiguration, error) {
	if value.TemplateRevision != 0 {
		return value.TemplateConfiguration, nil
	}
	template, err := s.store.FindTemplateByID(ctx, value.TemplateID)
	if err != nil {
		return domain.TemplateConfiguration{}, err
	}
	return template.Configuration, nil
}

// UpdateObservedState is called by reconciliation code, not by browser
// handlers. It keeps runtime truth separate from the desired state.
func (s *Service) UpdateObservedState(ctx context.Context, workspaceID, observedState, runtimeID, observedError string, observedAt time.Time) error {
	errorCode := ""
	if observedError != "" {
		errorCode = "runtime_observation_failed"
	}
	return s.store.UpdateWorkspaceObservedState(ctx, workspaceID, observedState, runtimeID, errorCode, observedError, observedAt, s.now().UTC())
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

func (s *Service) requireAdministratorActor(ctx context.Context, actorID string) error {
	user, err := s.store.FindUserByID(ctx, actorID)
	if err != nil {
		return err
	}
	if !user.IsAdministrator() || user.MustChangePassword {
		return ErrWorkspaceNotAuthorized
	}
	return nil
}

func roleAllowed(roles []domain.Role, role domain.Role) bool {
	for _, allowed := range roles {
		if allowed == role {
			return true
		}
	}
	return false
}

func (s *Service) templateAllowed(ctx context.Context, user domain.User, template domain.WorkspaceTemplate) (bool, error) {
	if !roleAllowed(template.AllowedRoles, user.Role) {
		return false, nil
	}
	if len(template.AllowedGroupIDs) == 0 {
		return template.GroupAccessMode != domain.GroupAccessInclude, nil
	}
	groupIDs, err := s.store.ListUserGroupIDs(ctx, user.ID)
	if err != nil {
		return false, err
	}
	memberships := make(map[string]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		memberships[groupID] = struct{}{}
	}
	matched := false
	for _, groupID := range template.AllowedGroupIDs {
		if _, ok := memberships[groupID]; ok {
			matched = true
			break
		}
	}
	if template.GroupAccessMode == domain.GroupAccessInclude {
		return matched, nil
	}
	return !matched, nil
}

func hasAccessMethod(methods []domain.AccessMethod, wanted domain.AccessMethod) bool {
	for _, method := range methods {
		if method == wanted {
			return true
		}
	}
	return false
}

func findDesktopService(configuration domain.TemplateConfiguration) (domain.TemplateService, bool) {
	for _, service := range configuration.Services {
		if service.Name == "desktop" && service.Protocol == "tcp" {
			return service, true
		}
	}
	return domain.TemplateService{}, false
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
