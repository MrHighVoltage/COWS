package workspace

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
)

var (
	configurationNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	environmentNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	placeholderPattern       = regexp.MustCompile(`\{\{([a-z][a-z0-9_.-]*)\}\}`)
	ErrPortPoolUnavailable   = errors.New("workspace port pool is unavailable")
)

func validateTemplateConfiguration(configuration domain.TemplateConfiguration) error {
	if len(configuration.Command) > 32 || len(configuration.Environment) > 64 || len(configuration.Secrets) > 32 || len(configuration.Mounts) > 16 || len(configuration.Services) > 16 {
		return ErrInvalidTemplate
	}
	for _, value := range configuration.Command {
		if len(value) == 0 || len(value) > 256 || hasControl(value) {
			return ErrInvalidTemplate
		}
	}
	environmentNames := make(map[string]struct{}, len(configuration.Environment))
	for _, value := range configuration.Environment {
		if !environmentNamePattern.MatchString(value.Name) || len(value.Value) > 4096 || hasControl(value.Value) {
			return ErrInvalidTemplate
		}
		if _, exists := environmentNames[value.Name]; exists {
			return ErrInvalidTemplate
		}
		environmentNames[value.Name] = struct{}{}
		if !validPlaceholders(value.Value, configuration) {
			return ErrInvalidTemplate
		}
	}
	secretNames := make(map[string]struct{}, len(configuration.Secrets))
	for _, value := range configuration.Secrets {
		if !configurationNamePattern.MatchString(value.Name) || value.Length < 0 || value.Length > 256 {
			return ErrInvalidTemplate
		}
		if _, exists := secretNames[value.Name]; exists {
			return ErrInvalidTemplate
		}
		secretNames[value.Name] = struct{}{}
		if value.Generate {
			if value.Value != "" || value.Length < 6 {
				return ErrInvalidTemplate
			}
		} else if value.Value == "" || len(value.Value) > 4096 || hasControl(value.Value) || value.Length != 0 {
			return ErrInvalidTemplate
		}
	}
	mountNames := make(map[string]struct{}, len(configuration.Mounts))
	for _, value := range configuration.Mounts {
		if !configurationNamePattern.MatchString(value.Name) || !validContainerPath(value.ContainerPath) {
			return ErrInvalidTemplate
		}
		if _, exists := mountNames[value.Name]; exists {
			return ErrInvalidTemplate
		}
		mountNames[value.Name] = struct{}{}
	}
	serviceNames := make(map[string]struct{}, len(configuration.Services))
	for _, value := range configuration.Services {
		if !configurationNamePattern.MatchString(value.Name) || (value.Protocol != "tcp" && value.Protocol != "udp") || value.ContainerPort < 1 || value.ContainerPort > 65535 || value.HostPortStart < 1024 || value.HostPortEnd > 65535 || value.HostPortStart > value.HostPortEnd || !configurationNamePattern.MatchString(value.PortPool) {
			return ErrInvalidTemplate
		}
		if value.PasswordSecret != "" {
			if !configurationNamePattern.MatchString(value.PasswordSecret) {
				return ErrInvalidTemplate
			}
			if _, ok := secretNames[value.PasswordSecret]; !ok {
				return ErrInvalidTemplate
			}
		}
		if _, exists := serviceNames[value.Name]; exists {
			return ErrInvalidTemplate
		}
		serviceNames[value.Name] = struct{}{}
	}
	return nil
}

func validPlaceholders(value string, configuration domain.TemplateConfiguration) bool {
	if strings.Count(value, "{{") != strings.Count(value, "}}") {
		return false
	}
	services := make(map[string]struct{}, len(configuration.Services))
	for _, service := range configuration.Services {
		services[service.Name] = struct{}{}
	}
	mounts := make(map[string]struct{}, len(configuration.Mounts))
	for _, mount := range configuration.Mounts {
		mounts[mount.Name] = struct{}{}
	}
	secrets := make(map[string]struct{}, len(configuration.Secrets))
	for _, secret := range configuration.Secrets {
		secrets[secret.Name] = struct{}{}
	}
	for _, match := range placeholderPattern.FindAllStringSubmatch(value, -1) {
		name := match[1]
		switch {
		case name == "cows.workspace_id", name == "cows.workspace_name":
		case strings.HasPrefix(name, "cows.service.") && strings.HasSuffix(name, ".port"):
			serviceName := strings.TrimSuffix(strings.TrimPrefix(name, "cows.service."), ".port")
			if _, ok := services[serviceName]; !ok {
				return false
			}
		case strings.HasPrefix(name, "cows.mount.") && strings.HasSuffix(name, ".path"):
			mountName := strings.TrimSuffix(strings.TrimPrefix(name, "cows.mount."), ".path")
			if _, ok := mounts[mountName]; !ok {
				return false
			}
		case strings.HasPrefix(name, "cows.secret."):
			secretName := strings.TrimPrefix(name, "cows.secret.")
			if _, ok := secrets[secretName]; !ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validContainerPath(value string) bool {
	return strings.HasPrefix(value, "/") && len(value) <= 256 && !hasControl(value) && value != "/" && !strings.Contains(value, "..")
}

func hasControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func (s *Service) reserveWorkspacePorts(ctx context.Context, workspaceID string, services []domain.TemplateService) error {
	for _, service := range services {
		rangeSize := service.HostPortEnd - service.HostPortStart + 1
		attempts := rangeSize
		if attempts > 256 {
			attempts = 256
		}
		reserved := false
		for attempt := 0; attempt < attempts; attempt++ {
			offset, err := rand.Int(rand.Reader, big.NewInt(int64(rangeSize)))
			if err != nil {
				return err
			}
			allocation := domain.PortAllocation{WorkspaceID: workspaceID, ServiceName: service.Name, Protocol: service.Protocol, ContainerPort: service.ContainerPort, PortPool: service.PortPool, HostPort: service.HostPortStart + int(offset.Int64())}
			if err := s.store.ReserveWorkspacePort(ctx, allocation); err == nil {
				reserved = true
				break
			} else if !errors.Is(err, repository.ErrConflict) {
				return err
			}
		}
		if !reserved {
			_ = s.store.ReleaseWorkspacePorts(ctx, workspaceID)
			return ErrPortPoolUnavailable
		}
	}
	return nil
}

func (s *Service) ensureWorkspacePorts(ctx context.Context, workspaceID string, configuration domain.TemplateConfiguration) error {
	allocations, err := s.store.ListWorkspacePortAllocations(ctx, workspaceID)
	if err != nil {
		return err
	}
	if len(allocations) == len(configuration.Services) {
		return nil
	}
	if err := s.store.ReleaseWorkspacePorts(ctx, workspaceID); err != nil {
		return err
	}
	return s.reserveWorkspacePorts(ctx, workspaceID, configuration.Services)
}

func resolveConfiguration(configuration domain.TemplateConfiguration, workspaceID, workspaceName string, allocations []domain.PortAllocation, secrets map[string]string) (runtime.WorkspaceConfiguration, error) {
	ports := make(map[string]string, len(allocations))
	for _, allocation := range allocations {
		ports[allocation.ServiceName] = strconv.Itoa(allocation.HostPort)
	}
	values := map[string]string{"cows.workspace_id": workspaceID, "cows.workspace_name": workspaceName}
	for service, port := range ports {
		values["cows.service."+service+".port"] = port
	}
	for name, secret := range secrets {
		values["cows.secret."+name] = secret
	}
	for _, mount := range configuration.Mounts {
		values["cows.mount."+mount.Name+".path"] = mount.ContainerPath
	}
	result := runtime.WorkspaceConfiguration{}
	for _, value := range configuration.Command {
		resolved, err := resolveValue(value, values)
		if err != nil {
			return runtime.WorkspaceConfiguration{}, err
		}
		result.Command = append(result.Command, resolved)
	}
	for _, value := range configuration.Environment {
		resolved, err := resolveValue(value.Value, values)
		if err != nil {
			return runtime.WorkspaceConfiguration{}, err
		}
		sensitive := value.Sensitive || value.Name == "VNC_PW" || containsSecretPlaceholder(value.Value)
		result.Environment = append(result.Environment, runtime.EnvironmentVariable{Name: value.Name, Value: resolved, Sensitive: sensitive})
	}
	for _, value := range configuration.Mounts {
		result.Mounts = append(result.Mounts, runtime.Mount{Name: value.Name, ContainerPath: value.ContainerPath, ReadOnly: value.ReadOnly})
	}
	for _, value := range configuration.Services {
		port, ok := ports[value.Name]
		if !ok {
			return runtime.WorkspaceConfiguration{}, ErrPortPoolUnavailable
		}
		resolvedPort, err := strconv.Atoi(port)
		if err != nil {
			return runtime.WorkspaceConfiguration{}, ErrPortPoolUnavailable
		}
		result.Ports = append(result.Ports, runtime.PortBinding{ServiceName: value.Name, Protocol: value.Protocol, ContainerPort: value.ContainerPort, HostPort: resolvedPort, HostIP: "127.0.0.1"})
	}
	return result, nil
}

const generatedSecretAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

func resolveTemplateSecrets(configuration domain.TemplateConfiguration) (map[string]string, error) {
	secrets := make(map[string]string, len(configuration.Secrets))
	for _, definition := range configuration.Secrets {
		if !definition.Generate {
			secrets[definition.Name] = definition.Value
			continue
		}
		value, err := generateSecret(definition.Length)
		if err != nil {
			return nil, fmt.Errorf("generate secret %q: %w", definition.Name, err)
		}
		secrets[definition.Name] = value
	}
	return secrets, nil
}

func generateSecret(length int) (string, error) {
	result := make([]byte, length)
	alphabetSize := big.NewInt(int64(len(generatedSecretAlphabet)))
	for index := range result {
		value, err := rand.Int(rand.Reader, alphabetSize)
		if err != nil {
			return "", err
		}
		result[index] = generatedSecretAlphabet[value.Int64()]
	}
	return string(result), nil
}

func containsSecretPlaceholder(value string) bool {
	for _, match := range placeholderPattern.FindAllStringSubmatch(value, -1) {
		if strings.HasPrefix(match[1], "cows.secret.") {
			return true
		}
	}
	return false
}

func resolveValue(value string, values map[string]string) (string, error) {
	resolved := placeholderPattern.ReplaceAllStringFunc(value, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")
		if replacement, ok := values[name]; ok {
			return replacement
		}
		return match
	})
	for _, match := range placeholderPattern.FindAllStringSubmatch(resolved, -1) {
		if _, ok := values[match[1]]; !ok {
			return "", ErrInvalidTemplate
		}
	}
	if hasControl(resolved) || len(resolved) > 8192 {
		return "", ErrInvalidTemplate
	}
	return resolved, nil
}
