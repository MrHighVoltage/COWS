package quota

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
)

var (
	ErrInvalidQuota         = errors.New("invalid quota")
	ErrQuotaUnavailable     = errors.New("user quota is not assigned")
	ErrQuotaExceeded        = errors.New("user quota exceeded")
	ErrCapacityUnavailable  = errors.New("host capacity unavailable")
	ErrCapacityInsufficient = errors.New("host capacity insufficient")
)

type Service struct {
	store repository.Store
	now   func() time.Time
}

type Input struct {
	MaxCPUMillis    int64
	MaxMemoryBytes  int64
	MaxStorageBytes int64
	MaxWorkspaces   int64
}

type HostSettingsInput struct {
	HostStorageBytes     int64
	ReservedCPUMillis    int64
	ReservedMemoryBytes  int64
	ReservedStorageBytes int64
}

func New(store repository.Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) List(ctx context.Context, actorID string) ([]domain.UserQuota, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.store.ListUserQuotas(ctx)
}

func (s *Service) Get(ctx context.Context, actorID, userID string) (domain.UserQuota, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.UserQuota{}, err
	}
	return s.store.FindUserQuota(ctx, userID)
}

func (s *Service) Set(ctx context.Context, actorID, userID string, input Input) (domain.UserQuota, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.UserQuota{}, err
	}
	if !validInput(input) {
		return domain.UserQuota{}, ErrInvalidQuota
	}
	if _, err := s.store.FindUserByID(ctx, userID); err != nil {
		return domain.UserQuota{}, err
	}
	existing, err := s.store.FindUserQuota(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		now := s.now().UTC()
		existing = domain.UserQuota{UserID: userID, CreatedAt: now}
	} else if err != nil {
		return domain.UserQuota{}, err
	}
	existing.MaxCPUMillis = input.MaxCPUMillis
	existing.MaxMemoryBytes = input.MaxMemoryBytes
	existing.MaxStorageBytes = input.MaxStorageBytes
	existing.MaxWorkspaces = input.MaxWorkspaces
	existing.UpdatedAt = s.now().UTC()
	if err := s.store.UpsertUserQuota(ctx, existing); err != nil {
		return domain.UserQuota{}, err
	}
	_ = s.store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "quota.updated", TargetType: "user", TargetID: userID})
	return existing, nil
}

// EnsureHostSettings creates the initial settings row without replacing an
// administrator's persisted values on later process starts.
func (s *Service) EnsureHostSettings(ctx context.Context, defaults HostSettingsInput) (domain.HostSettings, error) {
	settings, err := s.store.FindHostSettings(ctx)
	if err == nil {
		return settings, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return domain.HostSettings{}, err
	}
	if !validHostSettings(defaults) {
		return domain.HostSettings{}, ErrInvalidQuota
	}
	now := s.now().UTC()
	settings = domain.HostSettings{ID: 1, HostStorageBytes: defaults.HostStorageBytes, ReservedCPUMillis: defaults.ReservedCPUMillis, ReservedMemoryBytes: defaults.ReservedMemoryBytes, ReservedStorageBytes: defaults.ReservedStorageBytes, CreatedAt: now, UpdatedAt: now}
	if err := s.store.UpsertHostSettings(ctx, settings); err != nil {
		return domain.HostSettings{}, err
	}
	return settings, nil
}

func (s *Service) GetHostSettings(ctx context.Context, actorID string) (domain.HostSettings, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.HostSettings{}, err
	}
	return s.store.FindHostSettings(ctx)
}

func (s *Service) SetHostSettings(ctx context.Context, actorID string, input HostSettingsInput) (domain.HostSettings, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.HostSettings{}, err
	}
	if !validHostSettings(input) || (input.HostStorageBytes > 0 && input.ReservedStorageBytes > input.HostStorageBytes) {
		return domain.HostSettings{}, ErrInvalidQuota
	}
	existing, err := s.store.FindHostSettings(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		existing = domain.HostSettings{ID: 1, CreatedAt: s.now().UTC()}
	} else if err != nil {
		return domain.HostSettings{}, err
	}
	existing.HostStorageBytes = input.HostStorageBytes
	existing.ReservedCPUMillis = input.ReservedCPUMillis
	existing.ReservedMemoryBytes = input.ReservedMemoryBytes
	existing.ReservedStorageBytes = input.ReservedStorageBytes
	existing.UpdatedAt = s.now().UTC()
	if err := s.store.UpsertHostSettings(ctx, existing); err != nil {
		return domain.HostSettings{}, err
	}
	_ = s.store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "host_settings.updated", TargetType: "host", TargetID: "1"})
	return existing, nil
}

func (s *Service) requireAdministrator(ctx context.Context, actorID string) (domain.User, error) {
	user, err := s.store.FindUserByID(ctx, actorID)
	if err != nil {
		return domain.User{}, err
	}
	if user.MustChangePassword || !user.IsAdministrator() {
		return domain.User{}, errors.New("administrator permission required")
	}
	return user, nil
}

func validInput(input Input) bool {
	return input.MaxCPUMillis > 0 && input.MaxCPUMillis <= 1_000_000 && input.MaxMemoryBytes > 0 && input.MaxMemoryBytes <= 1<<50 && input.MaxStorageBytes > 0 && input.MaxStorageBytes <= 1<<60 && input.MaxWorkspaces > 0 && input.MaxWorkspaces <= 1_000_000
}

type Scheduler struct {
	store    repository.Store
	capacity runtime.CapacityProvider
}

func NewScheduler(store repository.Store, capacity runtime.CapacityProvider) *Scheduler {
	return &Scheduler{store: store, capacity: capacity}
}

func (s *Scheduler) CheckCreate(ctx context.Context, userID string, request domain.ResourceRequest) error {
	if request.CPUMillis <= 0 || request.MemoryBytes <= 0 || request.StorageBytes <= 0 {
		return ErrInvalidQuota
	}
	userQuota, err := s.store.FindUserQuota(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrQuotaUnavailable
	}
	if err != nil {
		return fmt.Errorf("load user quota: %w", err)
	}
	userAllocations, err := s.store.WorkspaceAllocations(ctx, userID)
	if err != nil {
		return err
	}
	if userAllocations.WorkspaceCount+1 > userQuota.MaxWorkspaces || exceeds(userAllocations.Resources.CPUMillis, request.CPUMillis, userQuota.MaxCPUMillis) || exceeds(userAllocations.Resources.MemoryBytes, request.MemoryBytes, userQuota.MaxMemoryBytes) || exceeds(userAllocations.Resources.StorageBytes, request.StorageBytes, userQuota.MaxStorageBytes) {
		return ErrQuotaExceeded
	}
	if s.capacity == nil {
		return ErrCapacityUnavailable
	}
	settings, err := s.store.FindHostSettings(ctx)
	if err != nil {
		return ErrCapacityUnavailable
	}
	host, err := s.capacity.HostCapacity(ctx)
	if err != nil || host.CPUMillis <= 0 || host.MemoryBytes <= 0 {
		return ErrCapacityUnavailable
	}
	if settings.HostStorageBytes <= 0 {
		return ErrCapacityUnavailable
	}
	host.StorageBytes = settings.HostStorageBytes
	allAllocations, err := s.store.AllWorkspaceAllocations(ctx)
	if err != nil {
		return err
	}
	if exceeds(settings.ReservedCPUMillis+allAllocations.Resources.CPUMillis, request.CPUMillis, host.CPUMillis) || exceeds(settings.ReservedMemoryBytes+allAllocations.Resources.MemoryBytes, request.MemoryBytes, host.MemoryBytes) || exceeds(settings.ReservedStorageBytes+allAllocations.Resources.StorageBytes, request.StorageBytes, host.StorageBytes) {
		return ErrCapacityInsufficient
	}
	return nil
}

func validHostSettings(input HostSettingsInput) bool {
	return input.HostStorageBytes >= 0 && input.HostStorageBytes <= 1<<60 && input.ReservedCPUMillis >= 0 && input.ReservedCPUMillis <= 1_000_000 && input.ReservedMemoryBytes >= 0 && input.ReservedMemoryBytes <= 1<<50 && input.ReservedStorageBytes >= 0 && input.ReservedStorageBytes <= 1<<60
}

func exceeds(current, requested, limit int64) bool {
	return current > limit-requested
}
