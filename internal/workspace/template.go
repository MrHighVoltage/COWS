package workspace

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/quota"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
)

var (
	ErrInvalidTemplate        = errors.New("invalid workspace template")
	ErrPasswordChangeRequired = errors.New("password change required")
)

// Template form field names. Template validation attributes each rejected
// value to one of these through domain.FieldError, and the administration
// form renders controls under exactly these names, so a rule and the input it
// marks cannot drift apart. The two memory fields are named for the MiB the
// form collects even though TemplateInput carries bytes, because the marked
// control is the one the administrator typed into.
const (
	FieldTemplateName              = "name"
	FieldTemplateDescription       = "description"
	FieldTemplateImageReference    = "image_reference"
	FieldTemplateImageDigest       = "image_digest"
	FieldTemplateDefaultCPU        = "default_cpu_millis"
	FieldTemplateMaxCPU            = "max_cpu_millis"
	FieldTemplateDefaultMemory     = "default_memory_mib"
	FieldTemplateMaxMemory         = "max_memory_mib"
	FieldTemplateInitialConnection = "initial_connection_hours"
	FieldTemplateStoppedRetention  = "stopped_retention_hours"
	FieldTemplateConfiguration     = "configuration_json"
	FieldTemplateAccessMethods     = "access_methods"
	FieldTemplateAllowedRoles      = "allowed_roles"
	FieldTemplateGroupAccessMode   = "group_access_mode"
	FieldTemplateAllowedGroupIDs   = "allowed_group_ids"
)

// invalidTemplateField attributes a rejected template value to the form
// control that carries it.
func invalidTemplateField(field, message string) error {
	return domain.InvalidField(ErrInvalidTemplate, field, message)
}

const (
	auditTemplateCreated  = "template.created"
	auditTemplateUpdated  = "template.updated"
	auditTemplateEnabled  = "template.enabled"
	auditTemplateDisabled = "template.disabled"
)

type TemplateInput struct {
	Name                            string
	Description                     string
	ImageReference                  string
	ImageDigest                     string
	DefaultCPUMillis                int64
	MaxCPUMillis                    int64
	DefaultMemoryBytes              int64
	MaxMemoryBytes                  int64
	ResourcesConfigurable           bool
	DefaultStorageBytes             int64
	InitialConnectionTimeoutSeconds int64
	StoppedRetentionSeconds         int64
	Configuration                   domain.TemplateConfiguration
	AccessMethods                   []domain.AccessMethod
	AllowedRoles                    []domain.Role
	GroupAccessMode                 string
	AllowedGroupIDs                 []string
	Enabled                         bool
}

type Service struct {
	store            repository.Store
	scheduler        *quota.Scheduler
	runtime          runtime.Runtime
	mountRoot        string
	mountArchiveRoot string
	storageUsage     *StorageUsageProvider
	resourceUsage    runtime.ResourceUsageRuntime
	networkIsolation bool
	fileAccessLocks  sync.Map
	// One process-wide admission lock; database reservations belong to
	// the future multi-instance deployment.
	admissionMu sync.Mutex
	now         func() time.Time
}

// SetNetworkIsolation configures the process-wide network policy during
// startup. It is intentionally not user-editable or template-editable.
func (s *Service) SetNetworkIsolation(enabled bool) { s.networkIsolation = enabled }

func New(store repository.Store, schedulers ...*quota.Scheduler) *Service {
	return newService(store, nil, schedulers...)
}

func NewWithRuntime(store repository.Store, runtimeAdapter runtime.Runtime, schedulers ...*quota.Scheduler) *Service {
	return newService(store, runtimeAdapter, schedulers...)
}

func NewWithRuntimeAndMountRoot(store repository.Store, runtimeAdapter runtime.Runtime, mountRoot string, schedulers ...*quota.Scheduler) *Service {
	return NewWithRuntimeAndMountRoots(store, runtimeAdapter, mountRoot, mountRoot+"-archive", schedulers...)
}

func NewWithRuntimeAndMountRoots(store repository.Store, runtimeAdapter runtime.Runtime, mountRoot, mountArchiveRoot string, schedulers ...*quota.Scheduler) *Service {
	return newServiceWithStorage(store, runtimeAdapter, mountRoot, mountArchiveRoot, nil, schedulers...)
}

func NewWithRuntimeAndMountRootsAndStorage(store repository.Store, runtimeAdapter runtime.Runtime, mountRoot, mountArchiveRoot string, storageProvider *StorageUsageProvider, schedulers ...*quota.Scheduler) *Service {
	return newServiceWithStorage(store, runtimeAdapter, mountRoot, mountArchiveRoot, storageProvider, schedulers...)
}

func newServiceWithStorage(store repository.Store, runtimeAdapter runtime.Runtime, mountRoot, mountArchiveRoot string, storageProvider *StorageUsageProvider, schedulers ...*quota.Scheduler) *Service {
	service := newService(store, runtimeAdapter, schedulers...)
	service.mountRoot = mountRoot
	service.mountArchiveRoot = mountArchiveRoot
	if storageProvider != nil {
		service.storageUsage = storageProvider
	} else if storageRuntime, ok := runtimeAdapter.(runtime.StorageUsageRuntime); ok {
		service.storageUsage = NewStorageUsageProvider(storageRuntime, mountRoot)
	}
	if usageRuntime, ok := runtimeAdapter.(runtime.ResourceUsageRuntime); ok {
		service.resourceUsage = usageRuntime
	}
	return service
}

func newService(store repository.Store, runtimeAdapter runtime.Runtime, schedulers ...*quota.Scheduler) *Service {
	var scheduler *quota.Scheduler
	if len(schedulers) > 0 {
		scheduler = schedulers[0]
	}
	return &Service{store: store, scheduler: scheduler, runtime: runtimeAdapter, now: time.Now}
}

func (s *Service) ListTemplates(ctx context.Context, actorID string) ([]domain.WorkspaceTemplate, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.store.ListTemplates(ctx)
}

func (s *Service) GetTemplate(ctx context.Context, actorID, id string) (domain.WorkspaceTemplate, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	return s.store.FindTemplateByID(ctx, id)
}

func (s *Service) CreateTemplate(ctx context.Context, actorID string, input TemplateInput) (domain.WorkspaceTemplate, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	input = normalizeTemplateResources(input)
	if err := validateTemplate(input); err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	if _, err := s.store.FindTemplateByName(ctx, strings.TrimSpace(input.Name)); err == nil {
		return domain.WorkspaceTemplate{}, repository.ErrConflict
	} else if !errors.Is(err, repository.ErrNotFound) {
		return domain.WorkspaceTemplate{}, err
	}
	id, err := newID()
	if err != nil {
		return domain.WorkspaceTemplate{}, fmt.Errorf("create template ID: %w", err)
	}
	now := s.now().UTC()
	template := domain.WorkspaceTemplate{
		ID:                              id,
		Name:                            strings.TrimSpace(input.Name),
		Description:                     strings.TrimSpace(input.Description),
		ImageReference:                  strings.TrimSpace(input.ImageReference),
		ImageDigest:                     strings.TrimSpace(input.ImageDigest),
		DefaultCPUMillis:                input.DefaultCPUMillis,
		MaxCPUMillis:                    input.MaxCPUMillis,
		DefaultMemoryBytes:              input.DefaultMemoryBytes,
		MaxMemoryBytes:                  input.MaxMemoryBytes,
		ResourcesConfigurable:           input.ResourcesConfigurable,
		DefaultStorageBytes:             input.DefaultStorageBytes,
		InitialConnectionTimeoutSeconds: input.InitialConnectionTimeoutSeconds,
		StoppedRetentionSeconds:         input.StoppedRetentionSeconds,
		Revision:                        1,
		Configuration:                   cloneTemplateConfiguration(input.Configuration),
		AccessMethods:                   append([]domain.AccessMethod(nil), input.AccessMethods...),
		AllowedRoles:                    append([]domain.Role(nil), input.AllowedRoles...),
		GroupAccessMode:                 normalizeGroupAccessMode(input.GroupAccessMode),
		AllowedGroupIDs:                 append([]string(nil), input.AllowedGroupIDs...),
		Enabled:                         input.Enabled,
		CreatedAt:                       now,
		UpdatedAt:                       now,
	}
	if err := s.store.CreateTemplate(ctx, template); err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditTemplateCreated, TargetType: "template", TargetID: id})
	return template, nil
}

func (s *Service) UpdateTemplate(ctx context.Context, actorID, id string, input TemplateInput) (domain.WorkspaceTemplate, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	input = normalizeTemplateResources(input)
	if err := validateTemplate(input); err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	existing, err := s.store.FindTemplateByID(ctx, id)
	if err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	if other, err := s.store.FindTemplateByName(ctx, strings.TrimSpace(input.Name)); err == nil && other.ID != id {
		return domain.WorkspaceTemplate{}, repository.ErrConflict
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return domain.WorkspaceTemplate{}, err
	}
	existing.Name = strings.TrimSpace(input.Name)
	existing.Description = strings.TrimSpace(input.Description)
	existing.ImageReference = strings.TrimSpace(input.ImageReference)
	existing.ImageDigest = strings.TrimSpace(input.ImageDigest)
	existing.DefaultCPUMillis = input.DefaultCPUMillis
	existing.MaxCPUMillis = input.MaxCPUMillis
	existing.DefaultMemoryBytes = input.DefaultMemoryBytes
	existing.MaxMemoryBytes = input.MaxMemoryBytes
	existing.ResourcesConfigurable = input.ResourcesConfigurable
	existing.DefaultStorageBytes = input.DefaultStorageBytes
	existing.InitialConnectionTimeoutSeconds = input.InitialConnectionTimeoutSeconds
	existing.StoppedRetentionSeconds = input.StoppedRetentionSeconds
	existing.Revision++
	if existing.Revision <= 0 {
		existing.Revision = 1
	}
	existing.Configuration = cloneTemplateConfiguration(input.Configuration)
	existing.AccessMethods = append([]domain.AccessMethod(nil), input.AccessMethods...)
	existing.AllowedRoles = append([]domain.Role(nil), input.AllowedRoles...)
	existing.GroupAccessMode = normalizeGroupAccessMode(input.GroupAccessMode)
	existing.AllowedGroupIDs = append([]string(nil), input.AllowedGroupIDs...)
	existing.Enabled = input.Enabled
	existing.UpdatedAt = s.now().UTC()
	if err := s.store.UpdateTemplate(ctx, existing); err != nil {
		return domain.WorkspaceTemplate{}, err
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: auditTemplateUpdated, TargetType: "template", TargetID: id})
	return existing, nil
}

func (s *Service) SetTemplateEnabled(ctx context.Context, actorID, id string, enabled bool) error {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return err
	}
	if _, err := s.store.FindTemplateByID(ctx, id); err != nil {
		return err
	}
	if err := s.store.SetTemplateEnabled(ctx, id, enabled, s.now().UTC()); err != nil {
		return err
	}
	eventType := auditTemplateDisabled
	if enabled {
		eventType = auditTemplateEnabled
	}
	s.recordAudit(ctx, domain.AuditEvent{ActorUserID: actorID, EventType: eventType, TargetType: "template", TargetID: id})
	return nil
}

func (s *Service) requireAdministrator(ctx context.Context, actorID string) (domain.User, error) {
	user, err := s.store.FindUserByID(ctx, actorID)
	if err != nil {
		return domain.User{}, err
	}
	if user.MustChangePassword {
		return domain.User{}, ErrPasswordChangeRequired
	}
	if !user.IsAdministrator() {
		return domain.User{}, errors.New("administrator permission required")
	}
	return user, nil
}

func (s *Service) recordAudit(ctx context.Context, event domain.AuditEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now().UTC()
	}
	_ = s.store.RecordAuditEvent(ctx, event)
}

// normalizeTemplateResources collapses the maximum onto the default for a
// template whose resources users cannot choose. A maximum only describes the
// top of a user-selectable range, so a fixed template has none:
// selectedResources ignores it when creating a workspace and the form freezes
// its control. Leaving a stale value there would advertise a ceiling that
// nothing enforces, and would reject a default raised above it by naming a
// control the administrator is not allowed to edit.
func normalizeTemplateResources(input TemplateInput) TemplateInput {
	if input.ResourcesConfigurable {
		return input
	}
	input.MaxCPUMillis = input.DefaultCPUMillis
	input.MaxMemoryBytes = input.DefaultMemoryBytes
	return input
}

// ValidateTemplateInput runs the same checks CreateTemplate and UpdateTemplate
// run, without storing anything. The web layer calls it when its own parsing
// already failed, so a submission whose numbers could not be read still gets
// marks on everything else that is wrong instead of only the unreadable
// fields.
func ValidateTemplateInput(input TemplateInput) error {
	return validateTemplate(normalizeTemplateResources(input))
}

// validateTemplate checks every submitted value and reports all of the ones
// it rejects together, each attributed to the control that carries it, so one
// submission of this long form marks everything that needs fixing instead of
// revealing the problems one at a time. It expects input to have been through
// normalizeTemplateResources.
func validateTemplate(input TemplateInput) error {
	problems := make([]error, 0, 8)
	// A fixed template's ceiling is its default, so report an out-of-range
	// ceiling on the default control: its own maximum control is frozen.
	cpuCeilingField, memoryCeilingField := FieldTemplateMaxCPU, FieldTemplateMaxMemory
	if !input.ResourcesConfigurable {
		cpuCeilingField, memoryCeilingField = FieldTemplateDefaultCPU, FieldTemplateDefaultMemory
	}
	if runeCount(input.Name) < 1 {
		problems = append(problems, invalidTemplateField(FieldTemplateName, "Enter a display name."))
	} else if runeCount(input.Name) > 100 {
		problems = append(problems, invalidTemplateField(FieldTemplateName, "Use at most 100 characters."))
	}
	if runeCount(input.Description) > 2000 {
		problems = append(problems, invalidTemplateField(FieldTemplateDescription, "Use at most 2000 characters."))
	}
	if !validImageReference(input.ImageReference) {
		problems = append(problems, invalidTemplateField(FieldTemplateImageReference, "Enter a reference of at most 255 characters without spaces or control characters, not starting with a dash."))
	}
	if !validDigest(input.ImageDigest) {
		problems = append(problems, invalidTemplateField(FieldTemplateImageDigest, "Leave this empty or enter sha256: followed by 64 lowercase hexadecimal characters."))
	}
	switch {
	case input.DefaultCPUMillis <= 0:
		problems = append(problems, invalidTemplateField(FieldTemplateDefaultCPU, "Enter at least 1 mCPU."))
	case input.MaxCPUMillis < input.DefaultCPUMillis:
		problems = append(problems, invalidTemplateField(FieldTemplateMaxCPU, "The maximum must be at least the default CPU value."))
	case input.MaxCPUMillis > 1_000_000:
		problems = append(problems, invalidTemplateField(cpuCeilingField, "Enter at most 1000000 mCPU."))
	}
	switch {
	case input.DefaultMemoryBytes <= 0:
		problems = append(problems, invalidTemplateField(FieldTemplateDefaultMemory, "Enter at least 1 MiB."))
	case input.MaxMemoryBytes < input.DefaultMemoryBytes:
		problems = append(problems, invalidTemplateField(FieldTemplateMaxMemory, "The maximum must be at least the default memory value."))
	case input.MaxMemoryBytes > 1<<50:
		problems = append(problems, invalidTemplateField(memoryCeilingField, "Enter at most 1073741824 MiB."))
	}
	if !validTimeout(input.InitialConnectionTimeoutSeconds) {
		problems = append(problems, invalidTemplateField(FieldTemplateInitialConnection, "Enter 0 to disable, or at most 87600 hours."))
	}
	if !validTimeout(input.StoppedRetentionSeconds) {
		problems = append(problems, invalidTemplateField(FieldTemplateStoppedRetention, "Enter 0 to disable, or at most 87600 hours."))
	}
	if err := validateTemplateConfiguration(input.Configuration); err != nil {
		problems = append(problems, err)
	}
	problems = append(problems, validateTemplateAccess(input)...)
	if input.GroupAccessMode != "" && input.GroupAccessMode != domain.GroupAccessInclude && input.GroupAccessMode != domain.GroupAccessExclude {
		problems = append(problems, invalidTemplateField(FieldTemplateGroupAccessMode, "Choose one of the offered group policies."))
	}
	if hasDuplicateString(input.AllowedGroupIDs) {
		problems = append(problems, invalidTemplateField(FieldTemplateAllowedGroupIDs, "Select each group at most once."))
	}
	return domain.InvalidFields(problems...)
}

// validateTemplateAccess covers the access-method and role checkbox groups,
// including the file-manager rule that spans the access methods and the
// container configuration.
func validateTemplateAccess(input TemplateInput) []error {
	problems := make([]error, 0, 3)
	switch {
	case len(input.AccessMethods) == 0:
		problems = append(problems, invalidTemplateField(FieldTemplateAccessMethods, "Select at least one access method."))
	case hasDuplicateAccessMethod(input.AccessMethods):
		problems = append(problems, invalidTemplateField(FieldTemplateAccessMethods, "Select each access method at most once."))
	default:
		for _, method := range input.AccessMethods {
			if !method.Valid() {
				problems = append(problems, invalidTemplateField(FieldTemplateAccessMethods, "Select only the offered access methods."))
				break
			}
		}
		if hasAccessMethod(input.AccessMethods, domain.AccessFiles) {
			fileMount := false
			for _, mount := range input.Configuration.Mounts {
				if mount.FileManager {
					fileMount = true
					break
				}
			}
			if !fileMount {
				// The rule spans two controls, so mark both: the access
				// method can be dropped or the mount can be added.
				problems = append(problems,
					invalidTemplateField(FieldTemplateAccessMethods, "The browser file manager needs a mount marked file_manager in the container configuration."),
					invalidTemplateField(FieldConfigurationMounts, "Mark one mount for the browser file manager (\"file_manager\": true), or clear the browser file manager access method."),
				)
			}
		}
	}
	switch {
	case len(input.AllowedRoles) == 0:
		problems = append(problems, invalidTemplateField(FieldTemplateAllowedRoles, "Select at least one role."))
	case hasDuplicateRole(input.AllowedRoles):
		problems = append(problems, invalidTemplateField(FieldTemplateAllowedRoles, "Select each role at most once."))
	default:
		for _, role := range input.AllowedRoles {
			if !role.Valid() {
				problems = append(problems, invalidTemplateField(FieldTemplateAllowedRoles, "Select only the offered roles."))
				break
			}
		}
	}
	return problems
}

func normalizeGroupAccessMode(value string) string {
	if value == "" {
		return domain.GroupAccessExclude
	}
	return value
}

func cloneTemplateConfiguration(value domain.TemplateConfiguration) domain.TemplateConfiguration {
	clone := domain.TemplateConfiguration{
		Command:      append([]string(nil), value.Command...),
		Environment:  append([]domain.TemplateEnvironment(nil), value.Environment...),
		Secrets:      append([]domain.TemplateSecret(nil), value.Secrets...),
		Mounts:       append([]domain.TemplateMount(nil), value.Mounts...),
		Services:     append([]domain.TemplateService(nil), value.Services...),
		TerminalUIDs: append([]int64(nil), value.TerminalUIDs...),
	}
	if value.ContainerUser != nil {
		user := *value.ContainerUser
		if value.ContainerUser.UID != nil {
			uid := *value.ContainerUser.UID
			user.UID = &uid
		}
		if value.ContainerUser.GID != nil {
			gid := *value.ContainerUser.GID
			user.GID = &gid
		}
		clone.ContainerUser = &user
	}
	return clone
}

func validTimeout(seconds int64) bool {
	return seconds >= 0 && seconds <= 10*365*24*60*60
}

func validImageReference(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsAny(value, " \t\r\n") || strings.HasPrefix(value, "-") {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func hasDuplicateAccessMethod(values []domain.AccessMethod) bool {
	seen := make(map[domain.AccessMethod]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func hasDuplicateRole(values []domain.Role) bool {
	seen := make(map[domain.Role]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func hasDuplicateString(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return true
		}
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func runeCount(value string) int {
	return utf8.RuneCountInString(strings.TrimSpace(value))
}

func newID() (string, error) {
	bytes := make([]byte, 18)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
