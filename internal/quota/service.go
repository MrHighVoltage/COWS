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
	reserved runtime.HostCapacity
}

type CapacityOverride struct {
	provider     runtime.CapacityProvider
	storageBytes int64
}

func NewCapacityOverride(provider runtime.CapacityProvider, storageBytes int64) runtime.CapacityProvider {
	return CapacityOverride{provider: provider, storageBytes: storageBytes}
}

func (c CapacityOverride) HostCapacity(ctx context.Context) (runtime.HostCapacity, error) {
	if c.provider == nil {
		return runtime.HostCapacity{}, runtime.ErrUnavailable
	}
	capacity, err := c.provider.HostCapacity(ctx)
	if err != nil {
		return runtime.HostCapacity{}, err
	}
	if c.storageBytes > 0 {
		capacity.StorageBytes = c.storageBytes
	}
	return capacity, nil
}

func NewScheduler(store repository.Store, capacity runtime.CapacityProvider, reserved runtime.HostCapacity) *Scheduler {
	return &Scheduler{store: store, capacity: capacity, reserved: reserved}
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
	host, err := s.capacity.HostCapacity(ctx)
	if err != nil || host.CPUMillis <= 0 || host.MemoryBytes <= 0 || host.StorageBytes <= 0 {
		return ErrCapacityUnavailable
	}
	allAllocations, err := s.store.AllWorkspaceAllocations(ctx)
	if err != nil {
		return err
	}
	if exceeds(s.reserved.CPUMillis+allAllocations.Resources.CPUMillis, request.CPUMillis, host.CPUMillis) || exceeds(s.reserved.MemoryBytes+allAllocations.Resources.MemoryBytes, request.MemoryBytes, host.MemoryBytes) || exceeds(s.reserved.StorageBytes+allAllocations.Resources.StorageBytes, request.StorageBytes, host.StorageBytes) {
		return ErrCapacityInsufficient
	}
	return nil
}

func exceeds(current, requested, limit int64) bool {
	return current > limit-requested
}
