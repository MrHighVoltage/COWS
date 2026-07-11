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
	"github.com/cows-project/cows/internal/quota"
	"github.com/cows-project/cows/internal/repository"
)

var (
	ErrInvalidTemplate        = errors.New("invalid workspace template")
	ErrPasswordChangeRequired = errors.New("password change required")
)

const (
	auditTemplateCreated  = "template.created"
	auditTemplateUpdated  = "template.updated"
	auditTemplateEnabled  = "template.enabled"
	auditTemplateDisabled = "template.disabled"
)

type TemplateInput struct {
	Name                string
	Description         string
	ImageReference      string
	ImageDigest         string
	DefaultCPUMillis    int64
	MaxCPUMillis        int64
	DefaultMemoryBytes  int64
	MaxMemoryBytes      int64
	DefaultStorageBytes int64
	AccessMethods       []domain.AccessMethod
	AllowedRoles        []domain.Role
	Enabled             bool
}

type Service struct {
	store     repository.Store
	scheduler *quota.Scheduler
	now       func() time.Time
}

func New(store repository.Store, schedulers ...*quota.Scheduler) *Service {
	var scheduler *quota.Scheduler
	if len(schedulers) > 0 {
		scheduler = schedulers[0]
	}
	return &Service{store: store, scheduler: scheduler, now: time.Now}
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
		ID:                  id,
		Name:                strings.TrimSpace(input.Name),
		Description:         strings.TrimSpace(input.Description),
		ImageReference:      strings.TrimSpace(input.ImageReference),
		ImageDigest:         strings.TrimSpace(input.ImageDigest),
		DefaultCPUMillis:    input.DefaultCPUMillis,
		MaxCPUMillis:        input.MaxCPUMillis,
		DefaultMemoryBytes:  input.DefaultMemoryBytes,
		MaxMemoryBytes:      input.MaxMemoryBytes,
		DefaultStorageBytes: input.DefaultStorageBytes,
		AccessMethods:       append([]domain.AccessMethod(nil), input.AccessMethods...),
		AllowedRoles:        append([]domain.Role(nil), input.AllowedRoles...),
		Enabled:             input.Enabled,
		CreatedAt:           now,
		UpdatedAt:           now,
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
	existing.DefaultStorageBytes = input.DefaultStorageBytes
	existing.AccessMethods = append([]domain.AccessMethod(nil), input.AccessMethods...)
	existing.AllowedRoles = append([]domain.Role(nil), input.AllowedRoles...)
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

func validateTemplate(input TemplateInput) error {
	if runeCount(input.Name) < 1 || runeCount(input.Name) > 100 {
		return ErrInvalidTemplate
	}
	if runeCount(input.Description) > 2000 {
		return ErrInvalidTemplate
	}
	if !validImageReference(input.ImageReference) || !validDigest(input.ImageDigest) {
		return ErrInvalidTemplate
	}
	if input.DefaultCPUMillis <= 0 || input.MaxCPUMillis < input.DefaultCPUMillis || input.MaxCPUMillis > 1_000_000 {
		return ErrInvalidTemplate
	}
	if input.DefaultMemoryBytes <= 0 || input.MaxMemoryBytes < input.DefaultMemoryBytes || input.MaxMemoryBytes > 1<<50 {
		return ErrInvalidTemplate
	}
	if input.DefaultStorageBytes <= 0 || input.DefaultStorageBytes > 1<<60 {
		return ErrInvalidTemplate
	}
	if len(input.AccessMethods) == 0 || hasDuplicateAccessMethod(input.AccessMethods) {
		return ErrInvalidTemplate
	}
	for _, method := range input.AccessMethods {
		if !method.Valid() {
			return ErrInvalidTemplate
		}
	}
	if len(input.AllowedRoles) == 0 || hasDuplicateRole(input.AllowedRoles) {
		return ErrInvalidTemplate
	}
	for _, role := range input.AllowedRoles {
		if !role.Valid() {
			return ErrInvalidTemplate
		}
	}
	return nil
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
