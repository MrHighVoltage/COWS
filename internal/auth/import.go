package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
)

var ErrInvalidUserImport = errors.New("invalid user import")

type ImportUserInput struct {
	Username    string
	Email       string
	DisplayName string
}

type ImportUserPreview struct {
	ImportUserInput
	Existing       bool
	ExistingUserID string
	EmailChanged   bool
	GroupsToAdd    []string
}

type ImportedUserResult struct {
	ImportUserInput
	UserID   string
	Existing bool
	Password string
}

func (s *Service) PreviewUserImport(ctx context.Context, actorID string, inputs []ImportUserInput, groupIDs []string) ([]ImportUserPreview, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	if err := s.validateImportInputs(ctx, inputs, groupIDs); err != nil {
		return nil, err
	}
	previews := make([]ImportUserPreview, 0, len(inputs))
	for _, input := range inputs {
		input.Username = normalizeUsername(input.Username)
		record, err := s.store.FindUserByUsername(ctx, input.Username)
		if errors.Is(err, repository.ErrNotFound) {
			previews = append(previews, ImportUserPreview{ImportUserInput: input})
			continue
		}
		if err != nil {
			return nil, err
		}
		groupIDsForUser, err := s.store.ListUserGroupIDs(ctx, record.User.ID)
		if err != nil {
			return nil, err
		}
		memberships := make(map[string]struct{}, len(groupIDsForUser))
		for _, groupID := range groupIDsForUser {
			memberships[groupID] = struct{}{}
		}
		toAdd := make([]string, 0, len(groupIDs))
		for _, groupID := range groupIDs {
			if _, exists := memberships[groupID]; !exists {
				toAdd = append(toAdd, groupID)
			}
		}
		previews = append(previews, ImportUserPreview{
			ImportUserInput: input,
			Existing:        true,
			ExistingUserID:  record.User.ID,
			EmailChanged:    input.Email != "" && input.Email != record.User.Email,
			GroupsToAdd:     toAdd,
		})
	}
	return previews, nil
}

func (s *Service) ImportUsers(ctx context.Context, actorID string, inputs []ImportUserInput, groupIDs []string) ([]ImportedUserResult, error) {
	if _, err := s.requireAdministrator(ctx, actorID); err != nil {
		return nil, err
	}
	if err := s.validateImportInputs(ctx, inputs, groupIDs); err != nil {
		return nil, err
	}
	entries := make([]repository.UserImportEntry, 0, len(inputs))
	results := make([]ImportedUserResult, 0, len(inputs))
	for _, input := range inputs {
		input.Username = normalizeUsername(input.Username)
		record, err := s.store.FindUserByUsername(ctx, input.Username)
		if err == nil {
			currentGroups, groupErr := s.store.ListUserGroupIDs(ctx, record.User.ID)
			if groupErr != nil {
				return nil, groupErr
			}
			memberships := make(map[string]struct{}, len(currentGroups))
			for _, groupID := range currentGroups {
				memberships[groupID] = struct{}{}
			}
			toAdd := make([]string, 0, len(groupIDs))
			for _, groupID := range groupIDs {
				if _, exists := memberships[groupID]; !exists {
					toAdd = append(toAdd, groupID)
				}
			}
			updated := record.User
			updated.Email = strings.TrimSpace(input.Email)
			updated.UpdatedAt = s.now().UTC()
			entries = append(entries, repository.UserImportEntry{User: updated, GroupIDs: toAdd, Existing: true})
			results = append(results, ImportedUserResult{ImportUserInput: input, UserID: record.User.ID, Existing: true})
			continue
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		password, passwordErr := GenerateTemporaryPassword(20)
		if passwordErr != nil {
			return nil, passwordErr
		}
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if hashErr != nil {
			return nil, hashErr
		}
		user, _, prepareErr := s.prepareUser(ctx, CreateUserInput{Username: input.Username, Email: input.Email, DisplayName: input.DisplayName, Password: password, Role: domain.RoleUser}, true)
		if prepareErr != nil {
			return nil, prepareErr
		}
		entries = append(entries, repository.UserImportEntry{User: user, PasswordHash: string(hash), GroupIDs: append([]string(nil), groupIDs...)})
		results = append(results, ImportedUserResult{ImportUserInput: input, UserID: user.ID, Password: password})
	}
	if err := s.store.ImportUsers(ctx, entries); err != nil {
		return nil, err
	}
	for _, result := range results {
		event := domain.AuditEvent{ActorUserID: actorID, EventType: "user.imported", TargetType: "user", TargetID: result.UserID}
		if result.Existing {
			event.EventType = "user.import_updated"
		}
		s.recordAudit(ctx, event)
	}
	return results, nil
}

func (s *Service) validateImportInputs(ctx context.Context, inputs []ImportUserInput, groupIDs []string) error {
	if len(inputs) == 0 || len(inputs) > 1000 {
		return ErrInvalidUserImport
	}
	seenUsers := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		username := normalizeUsername(input.Username)
		if !validUsername(username) || !validEmail(input.Email) || !validDisplayName(input.DisplayName) {
			return ErrInvalidUserImport
		}
		if _, exists := seenUsers[username]; exists {
			return ErrInvalidUserImport
		}
		seenUsers[username] = struct{}{}
	}
	seenGroups := make(map[string]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID == "" {
			return ErrInvalidUserImport
		}
		if _, exists := seenGroups[groupID]; exists {
			return ErrInvalidUserImport
		}
		seenGroups[groupID] = struct{}{}
		if _, err := s.store.FindGroupByID(ctx, groupID); err != nil {
			return ErrInvalidUserImport
		}
	}
	return nil
}

const temporaryPasswordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*"

func GenerateTemporaryPassword(length int) (string, error) {
	if length < 12 || length > 72 {
		return "", ErrInvalidUserImport
	}
	result := make([]byte, length)
	alphabetSize := big.NewInt(int64(len(temporaryPasswordAlphabet)))
	for index := range result {
		value, err := rand.Int(rand.Reader, alphabetSize)
		if err != nil {
			return "", err
		}
		result[index] = temporaryPasswordAlphabet[value.Int64()]
	}
	return string(result), nil
}
