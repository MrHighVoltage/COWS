package quota

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	ErrStorageUnavailable   = errors.New("workspace storage usage unavailable")
)

// Quota and host-settings form field names. Validation attributes each
// rejected value to one of these through domain.FieldError, and the quota and
// host-settings forms render controls under exactly these names. They are
// named for the MiB and GiB the forms collect even though Input and
// HostSettingsInput carry bytes, because the marked control is the one the
// administrator typed into.
const (
	FieldMaxCPUMillis            = "max_cpu_millis"
	FieldMaxMemoryMiB            = "max_memory_mib"
	FieldMaxStorageGiB           = "max_storage_gib"
	FieldMaxWorkspaces           = "max_workspaces"
	FieldMaxRunningWorkspaces    = "max_running_workspaces"
	FieldHostStorageGiB          = "host_storage_gib"
	FieldCPUOverbookingFactor    = "cpu_overbooking_factor"
	FieldMemoryOverbookingFactor = "memory_overbooking_factor"
	FieldReservedStorageGiB      = "reserved_storage_gib"
)

// invalidQuotaField attributes a rejected quota or host-settings value to the
// form control that carries it.
func invalidQuotaField(field, message string) error {
	return domain.InvalidField(ErrInvalidQuota, field, message)
}

type CapacityInsufficientError struct {
	Resource  string
	Available int64
	Requested int64
}

func (e *CapacityInsufficientError) Error() string {
	return fmt.Sprintf("host %s capacity insufficient: available=%d requested=%d", e.Resource, e.Available, e.Requested)
}

func (e *CapacityInsufficientError) Unwrap() error { return ErrCapacityInsufficient }

type QuotaInsufficientError struct {
	Resource  string
	Current   int64
	Limit     int64
	Requested int64
}

func (e *QuotaInsufficientError) Error() string {
	return fmt.Sprintf("quota %s insufficient: current=%d limit=%d requested=%d", e.Resource, e.Current, e.Limit, e.Requested)
}

func (e *QuotaInsufficientError) Unwrap() error { return ErrQuotaExceeded }

type Service struct {
	store repository.Store
	now   func() time.Time
}

type Input struct {
	MaxCPUMillis         int64
	MaxMemoryBytes       int64
	MaxStorageBytes      int64
	MaxWorkspaces        int64
	MaxRunningWorkspaces int64
}

type HostSettingsInput struct {
	HostStorageBytes        int64
	CPUOverbookingFactor    float64
	MemoryOverbookingFactor float64
	ReservedStorageBytes    int64
}

const (
	MinOverbookingFactor = 0.1
	MaxOverbookingFactor = 1_000
)

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

func (s *Service) GetForUser(ctx context.Context, userID string) (domain.UserQuota, error) {
	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return domain.UserQuota{}, err
	}
	if user.Disabled {
		return domain.UserQuota{}, errors.New("user is disabled")
	}
	return effectiveQuota(ctx, s.store, userID)
}

func (s *Service) GetGroup(ctx context.Context, actorID, groupID string) (domain.GroupQuota, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.GroupQuota{}, err
	}
	return s.store.FindGroupQuota(ctx, groupID)
}

func (s *Service) SetGroup(ctx context.Context, actorID, groupID string, input Input) (domain.GroupQuota, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.GroupQuota{}, err
	}
	if err := ValidateInput(input); err != nil {
		return domain.GroupQuota{}, err
	}
	if _, err := s.store.FindGroupByID(ctx, groupID); err != nil {
		return domain.GroupQuota{}, err
	}
	existing, err := s.store.FindGroupQuota(ctx, groupID)
	if errors.Is(err, repository.ErrNotFound) {
		existing = domain.GroupQuota{GroupID: groupID, CreatedAt: s.now().UTC()}
	} else if err != nil {
		return domain.GroupQuota{}, err
	}
	existing.MaxCPUMillis = input.MaxCPUMillis
	existing.MaxMemoryBytes = input.MaxMemoryBytes
	existing.MaxStorageBytes = input.MaxStorageBytes
	existing.MaxWorkspaces = input.MaxWorkspaces
	existing.MaxRunningWorkspaces = input.MaxRunningWorkspaces
	existing.UpdatedAt = s.now().UTC()
	if err := s.store.UpsertGroupQuota(ctx, existing); err != nil {
		return domain.GroupQuota{}, err
	}
	_ = s.store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "quota.group_updated", TargetType: "group", TargetID: groupID})
	return existing, nil
}

func (s *Service) UnsetGroup(ctx context.Context, actorID, groupID string) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if _, err := s.store.FindGroupByID(ctx, groupID); err != nil {
		return err
	}
	if err := s.store.DeleteGroupQuota(ctx, groupID); err != nil {
		return err
	}
	_ = s.store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "quota.group_removed", TargetType: "group", TargetID: groupID})
	return nil
}

func effectiveQuota(ctx context.Context, store repository.Store, userID string) (domain.UserQuota, error) {
	if quota, err := store.FindUserQuota(ctx, userID); err == nil {
		return quota, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return domain.UserQuota{}, err
	}
	groupQuotas, err := store.ListGroupQuotasForUser(ctx, userID)
	if err != nil {
		return domain.UserQuota{}, err
	}
	if len(groupQuotas) == 0 {
		return domain.UserQuota{}, repository.ErrNotFound
	}
	now := time.Now().UTC()
	return domain.UserQuota{
		UserID:               userID,
		MaxCPUMillis:         aggregateLimit(groupQuotas, func(value domain.GroupQuota) int64 { return value.MaxCPUMillis }, 1_000_000),
		MaxMemoryBytes:       aggregateLimit(groupQuotas, func(value domain.GroupQuota) int64 { return value.MaxMemoryBytes }, 1<<50),
		MaxStorageBytes:      aggregateLimit(groupQuotas, func(value domain.GroupQuota) int64 { return value.MaxStorageBytes }, 1<<60),
		MaxWorkspaces:        aggregateLimit(groupQuotas, func(value domain.GroupQuota) int64 { return value.MaxWorkspaces }, 1_000_000),
		MaxRunningWorkspaces: aggregateLimit(groupQuotas, func(value domain.GroupQuota) int64 { return value.MaxRunningWorkspaces }, 1_000_000),
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

func aggregateLimit(values []domain.GroupQuota, field func(domain.GroupQuota) int64, maximum int64) int64 {
	var total int64
	for _, value := range values {
		limit := field(value)
		if limit == 0 {
			return 0
		}
		if total > maximum-limit {
			return maximum
		}
		total += limit
	}
	return total
}

func (s *Service) Set(ctx context.Context, actorID, userID string, input Input) (domain.UserQuota, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.UserQuota{}, err
	}
	if err := ValidateInput(input); err != nil {
		return domain.UserQuota{}, err
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
	existing.MaxRunningWorkspaces = input.MaxRunningWorkspaces
	existing.UpdatedAt = s.now().UTC()
	if err := s.store.UpsertUserQuota(ctx, existing); err != nil {
		return domain.UserQuota{}, err
	}
	_ = s.store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "quota.updated", TargetType: "user", TargetID: userID})
	return existing, nil
}

func (s *Service) Unset(ctx context.Context, actorID, userID string) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if _, err := s.store.FindUserByID(ctx, userID); err != nil {
		return err
	}
	if err := s.store.DeleteUserQuota(ctx, userID); err != nil {
		return err
	}
	_ = s.store.RecordAuditEvent(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: "quota.removed", TargetType: "user", TargetID: userID})
	return nil
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
	if defaults.CPUOverbookingFactor == 0 {
		defaults.CPUOverbookingFactor = 1
	}
	if defaults.MemoryOverbookingFactor == 0 {
		defaults.MemoryOverbookingFactor = 1
	}
	if err := validateHostSettings(defaults); err != nil {
		return domain.HostSettings{}, err
	}
	now := s.now().UTC()
	settings = domain.HostSettings{ID: 1, HostStorageBytes: defaults.HostStorageBytes, CPUOverbookingFactor: defaults.CPUOverbookingFactor, MemoryOverbookingFactor: defaults.MemoryOverbookingFactor, ReservedStorageBytes: defaults.ReservedStorageBytes, CreatedAt: now, UpdatedAt: now}
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
	if err := ValidateHostSettings(input); err != nil {
		return domain.HostSettings{}, err
	}
	existing, err := s.store.FindHostSettings(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		existing = domain.HostSettings{ID: 1, CreatedAt: s.now().UTC()}
	} else if err != nil {
		return domain.HostSettings{}, err
	}
	existing.HostStorageBytes = input.HostStorageBytes
	existing.CPUOverbookingFactor = input.CPUOverbookingFactor
	existing.MemoryOverbookingFactor = input.MemoryOverbookingFactor
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

// ValidateInput exposes the same bounds used by administrator quota changes
// to validate server-side registration defaults during configuration loading.
// Every rejected limit is attributed to its form control and they are reported
// together, so one submission of the five-field quota editor marks each limit
// that is out of range.
func ValidateInput(input Input) error {
	return domain.InvalidFields(
		boundedLimit(FieldMaxCPUMillis, input.MaxCPUMillis, 1_000_000, "1000000 mCPU"),
		boundedLimit(FieldMaxMemoryMiB, input.MaxMemoryBytes, 1<<50, "1073741824 MiB"),
		boundedLimit(FieldMaxStorageGiB, input.MaxStorageBytes, 1<<60, "1073741824 GiB"),
		boundedLimit(FieldMaxWorkspaces, input.MaxWorkspaces, 1_000_000, "1000000 workspaces"),
		boundedLimit(FieldMaxRunningWorkspaces, input.MaxRunningWorkspaces, 1_000_000, "1000000 workspaces"),
	)
}

// boundedLimit reports whether one quota limit sits in [0, limit]. Zero always
// means unlimited, so it is never out of range.
func boundedLimit(field string, value, limit int64, ceiling string) error {
	if value < 0 {
		return invalidQuotaField(field, "Enter zero for unlimited, or a positive value.")
	}
	if value > limit {
		return invalidQuotaField(field, "Enter at most "+ceiling+", or zero for unlimited.")
	}
	return nil
}

type Scheduler struct {
	store    repository.Store
	capacity runtime.CapacityProvider
	storage  StorageUsageProvider
}

type StorageUsageProvider interface {
	WorkspaceStorageUsage(ctx context.Context, value domain.Workspace) (int64, error)
}

func NewScheduler(store repository.Store, capacity runtime.CapacityProvider, storage ...StorageUsageProvider) *Scheduler {
	var provider StorageUsageProvider
	if len(storage) > 0 {
		provider = storage[0]
	}
	return &Scheduler{store: store, capacity: capacity, storage: provider}
}

func (s *Scheduler) CheckCreate(ctx context.Context, userID string, request domain.ResourceRequest) error {
	if request.CPUMillis <= 0 || request.MemoryBytes <= 0 {
		return ErrInvalidQuota
	}
	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("load quota user: %w", err)
	}
	userQuota, err := effectiveQuota(ctx, s.store, userID)
	quotaAssigned := true
	if errors.Is(err, repository.ErrNotFound) {
		quotaAssigned = false
		if !user.IsAdministrator() {
			return ErrQuotaUnavailable
		}
	} else if err != nil {
		return fmt.Errorf("load user quota: %w", err)
	}
	userAllocations, err := s.store.WorkspaceAllocations(ctx, userID)
	if err != nil {
		return err
	}
	if quotaAssigned && exceedsQuota(userAllocations, request, userQuota) {
		return ErrQuotaExceeded
	}
	if quotaAssigned && userQuota.MaxStorageBytes > 0 {
		userWorkspaces, err := s.store.ListWorkspacesForUser(ctx, userID)
		if err != nil {
			return err
		}
		userStorage, err := s.measureStorage(ctx, userWorkspaces)
		if err != nil {
			return err
		}
		if userStorage >= userQuota.MaxStorageBytes {
			return ErrQuotaExceeded
		}
	}
	available, err := s.Available(ctx, userID)
	if err != nil {
		return err
	}
	if request.CPUMillis > available.CPUMillis {
		return &CapacityInsufficientError{Resource: "CPU", Available: available.CPUMillis, Requested: request.CPUMillis}
	}
	if request.MemoryBytes > available.MemoryBytes {
		return &CapacityInsufficientError{Resource: "memory", Available: available.MemoryBytes, Requested: request.MemoryBytes}
	}
	return nil
}

// Available returns the lower of current host capacity and the user's
// remaining CPU and memory quota. It is advisory for the UI.
func (s *Scheduler) Available(ctx context.Context, userID string) (domain.ResourceRequest, error) {
	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return domain.ResourceRequest{}, fmt.Errorf("load quota user: %w", err)
	}
	userQuota, err := effectiveQuota(ctx, s.store, userID)
	quotaAssigned := true
	if errors.Is(err, repository.ErrNotFound) {
		quotaAssigned = false
		if !user.IsAdministrator() {
			return domain.ResourceRequest{}, ErrQuotaUnavailable
		}
	} else if err != nil {
		return domain.ResourceRequest{}, fmt.Errorf("load user quota: %w", err)
	}
	available, err := s.hostAvailable(ctx)
	if err != nil {
		return domain.ResourceRequest{}, err
	}
	if quotaAssigned {
		userAllocations, err := s.store.WorkspaceAllocations(ctx, userID)
		if err != nil {
			return domain.ResourceRequest{}, err
		}
		if userQuota.MaxCPUMillis > 0 && userQuota.MaxCPUMillis-userAllocations.Resources.CPUMillis < available.CPUMillis {
			available.CPUMillis = userQuota.MaxCPUMillis - userAllocations.Resources.CPUMillis
		}
		if userQuota.MaxMemoryBytes > 0 && userQuota.MaxMemoryBytes-userAllocations.Resources.MemoryBytes < available.MemoryBytes {
			available.MemoryBytes = userQuota.MaxMemoryBytes - userAllocations.Resources.MemoryBytes
		}
	}
	if available.CPUMillis < 0 {
		available.CPUMillis = 0
	}
	if available.MemoryBytes < 0 {
		available.MemoryBytes = 0
	}
	return available, nil
}

func (s *Scheduler) CheckStart(ctx context.Context, userID string, request domain.ResourceRequest) error {
	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("load quota user: %w", err)
	}
	userQuota, err := effectiveQuota(ctx, s.store, userID)
	quotaAssigned := true
	if errors.Is(err, repository.ErrNotFound) {
		quotaAssigned = false
		if !user.IsAdministrator() {
			return ErrQuotaUnavailable
		}
	} else if err != nil {
		return fmt.Errorf("load user quota: %w", err)
	}
	if quotaAssigned {
		allocations, err := s.store.WorkspaceAllocations(ctx, userID)
		if err != nil {
			return err
		}
		if userQuota.MaxRunningWorkspaces > 0 && allocations.RunningWorkspaceCount >= userQuota.MaxRunningWorkspaces {
			return &QuotaInsufficientError{Resource: "running workspaces", Current: allocations.RunningWorkspaceCount, Limit: userQuota.MaxRunningWorkspaces, Requested: 1}
		}
		if exceeds(allocations.Resources.CPUMillis, request.CPUMillis, userQuota.MaxCPUMillis) {
			return &QuotaInsufficientError{Resource: "CPU", Current: allocations.Resources.CPUMillis, Limit: userQuota.MaxCPUMillis, Requested: request.CPUMillis}
		}
		if exceeds(allocations.Resources.MemoryBytes, request.MemoryBytes, userQuota.MaxMemoryBytes) {
			return &QuotaInsufficientError{Resource: "memory", Current: allocations.Resources.MemoryBytes, Limit: userQuota.MaxMemoryBytes, Requested: request.MemoryBytes}
		}
	}
	available, err := s.hostAvailable(ctx)
	if err != nil {
		return err
	}
	if request.CPUMillis > available.CPUMillis {
		return &CapacityInsufficientError{Resource: "CPU", Available: available.CPUMillis, Requested: request.CPUMillis}
	}
	if request.MemoryBytes > available.MemoryBytes {
		return &CapacityInsufficientError{Resource: "memory", Available: available.MemoryBytes, Requested: request.MemoryBytes}
	}
	return nil
}

func (s *Scheduler) hostAvailable(ctx context.Context) (domain.ResourceRequest, error) {
	if s.capacity == nil {
		return domain.ResourceRequest{}, ErrCapacityUnavailable
	}
	settings, err := s.store.FindHostSettings(ctx)
	if err != nil {
		return domain.ResourceRequest{}, ErrCapacityUnavailable
	}
	host, err := s.capacity.HostCapacity(ctx)
	if err != nil || host.CPUMillis <= 0 || host.MemoryBytes <= 0 {
		return domain.ResourceRequest{}, ErrCapacityUnavailable
	}
	allAllocations, err := s.store.AllWorkspaceAllocations(ctx)
	if err != nil {
		return domain.ResourceRequest{}, err
	}
	available := domain.ResourceRequest{
		CPUMillis:   ScaleCapacity(host.CPUMillis, settings.CPUOverbookingFactor) - allAllocations.Resources.CPUMillis,
		MemoryBytes: ScaleCapacity(host.MemoryBytes, settings.MemoryOverbookingFactor) - allAllocations.Resources.MemoryBytes,
	}
	if available.CPUMillis < 0 {
		available.CPUMillis = 0
	}
	if available.MemoryBytes < 0 {
		available.MemoryBytes = 0
	}
	return available, nil
}

func (s *Scheduler) measureStorage(ctx context.Context, userWorkspaces []domain.Workspace) (int64, error) {
	if s.storage == nil {
		return 0, ErrStorageUnavailable
	}
	var total int64
	for _, value := range userWorkspaces {
		bytes, err := s.storage.WorkspaceStorageUsage(ctx, value)
		if err != nil || bytes < 0 {
			return 0, ErrStorageUnavailable
		}
		total += bytes
	}
	return total, nil
}

// ValidateHostSettings runs the same checks SetHostSettings runs, without
// storing anything, so the web layer can ask what else a submission got wrong
// when its own parsing already failed. It covers one rule more than
// validateHostSettings: reserved storage has to fit inside the host total,
// which only applies to an administrator's edit and not to the configured
// startup defaults.
func ValidateHostSettings(input HostSettingsInput) error {
	problems := []error{validateHostSettings(input)}
	if input.HostStorageBytes > 0 && input.ReservedStorageBytes > input.HostStorageBytes {
		problems = append(problems, invalidQuotaField(FieldReservedStorageGiB, "Reserved storage cannot exceed the allocatable host storage."))
	}
	return domain.InvalidFields(problems...)
}

// validateHostSettings reports every host setting that is out of range, each
// attributed to its form control.
func validateHostSettings(input HostSettingsInput) error {
	return domain.InvalidFields(
		boundedLimit(FieldHostStorageGiB, input.HostStorageBytes, 1<<60, "1073741824 GiB"),
		overbookingFactor(FieldCPUOverbookingFactor, input.CPUOverbookingFactor),
		overbookingFactor(FieldMemoryOverbookingFactor, input.MemoryOverbookingFactor),
		boundedLimit(FieldReservedStorageGiB, input.ReservedStorageBytes, 1<<60, "1073741824 GiB"),
	)
}

func overbookingFactor(field string, value float64) error {
	if !validOverbookingFactor(value) {
		return invalidQuotaField(field, fmt.Sprintf("Enter a factor between %v and %v.", float64(MinOverbookingFactor), float64(MaxOverbookingFactor)))
	}
	return nil
}

func validOverbookingFactor(value float64) bool {
	return value >= MinOverbookingFactor && value <= MaxOverbookingFactor && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// ScaleCapacity applies an overbooking factor to a raw host capacity,
// yielding the allocatable ceiling COWS admits workspaces against.
func ScaleCapacity(value int64, factor float64) int64 {
	if value <= 0 || factor <= 0 {
		return 0
	}
	if factor > float64(math.MaxInt64)/float64(value) {
		return math.MaxInt64
	}
	return int64(float64(value) * factor)
}

func exceeds(current, requested, limit int64) bool {
	return limit > 0 && current > limit-requested
}

func exceedsQuota(current domain.AllocationSummary, request domain.ResourceRequest, limit domain.UserQuota) bool {
	return (limit.MaxWorkspaces > 0 && current.WorkspaceCount+1 > limit.MaxWorkspaces) ||
		exceeds(current.Resources.CPUMillis, request.CPUMillis, limit.MaxCPUMillis) ||
		exceeds(current.Resources.MemoryBytes, request.MemoryBytes, limit.MaxMemoryBytes)
}
