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
	containerUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,63}$`)
	mountNamePartPattern     = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	placeholderPattern       = regexp.MustCompile(`\{\{([a-z][a-z0-9_.-]*)\}\}`)
	ErrPortPoolUnavailable   = errors.New("workspace port pool is unavailable")
)

// Container-configuration field names. The template editor's form mode renders
// one control per configuration value under these names, so a rejection marks
// the exact control at fault, down to the row of a repeated entry. The JSON
// mode has one control for the whole document, and the web layer folds every
// name starting with FieldConfigurationPrefix onto FieldTemplateConfiguration
// there; that is why each message still names the element it describes.
const (
	FieldConfigurationPrefix                = "config_"
	FieldConfigurationCommand               = "config_command"
	FieldConfigurationTerminalUIDs          = "config_terminal_uids"
	FieldConfigurationEnvironment           = "config_env"
	FieldConfigurationSecrets               = "config_secret"
	FieldConfigurationMounts                = "config_mount"
	FieldConfigurationUser                  = "config_user"
	FieldConfigurationUserUID               = "config_user_uid"
	FieldConfigurationUserGID               = "config_user_gid"
	FieldConfigurationUserDisplayName       = "config_user_display_name"
	FieldConfigurationUserHome              = "config_user_home"
	FieldConfigurationUserShell             = "config_user_shell"
	FieldConfigurationDesktop               = "config_desktop"
	FieldConfigurationDesktopContainerPort  = "config_desktop_container_port"
	FieldConfigurationDesktopPortPool       = "config_desktop_port_pool"
	FieldConfigurationDesktopHostPortStart  = "config_desktop_host_port_start"
	FieldConfigurationDesktopHostPortEnd    = "config_desktop_host_port_end"
	FieldConfigurationDesktopPasswordSecret = "config_desktop_password_secret"
)

// Attributes of one repeated configuration entry. ConfigurationEntryField
// combines them with a list field (FieldConfigurationEnvironment,
// FieldConfigurationSecrets, or FieldConfigurationMounts) and the entry's
// position.
const (
	EntryName          = "name"
	EntryValue         = "value"
	EntrySensitive     = "sensitive"
	EntryGenerate      = "generate"
	EntryLength        = "length"
	EntryType          = "type"
	EntryContainerPath = "container_path"
	EntryReadOnly      = "read_only"
	EntryFileManager   = "file_manager"
	EntryNamePrefix    = "name_prefix"
	EntryNameSuffix    = "name_suffix"
)

// ConfigurationEntryField names the control for one attribute of the entry at
// index in list, such as "config_mount_2_container_path".
func ConfigurationEntryField(list string, index int, attribute string) string {
	return list + "_" + strconv.Itoa(index) + "_" + attribute
}

// Configuration list caps. The form editor stops offering another row at
// these, and validation rejects a longer list from either editor.
const (
	MaxConfigurationCommand      = 32
	MaxConfigurationEnvironment  = 64
	MaxConfigurationSecrets      = 32
	MaxConfigurationMounts       = 16
	MaxConfigurationServices     = 16
	MaxConfigurationTerminalUIDs = 32
)

const maxContainerID = 2147483647

func invalidConfiguration(field, format string, args ...any) error {
	return invalidTemplateField(field, fmt.Sprintf(format, args...))
}

// validateTemplateConfiguration checks the administrator-only typed
// configuration. It reports every value it rejects, each on its own control,
// so one submission of the form editor marks all of them. A list longer than
// its cap is rejected before any of its entries is looked at.
func validateTemplateConfiguration(configuration domain.TemplateConfiguration) error {
	switch {
	case len(configuration.Command) > MaxConfigurationCommand:
		return invalidConfiguration(FieldConfigurationCommand, "%q accepts at most %d arguments.", "command", MaxConfigurationCommand)
	case len(configuration.Environment) > MaxConfigurationEnvironment:
		return invalidConfiguration(FieldConfigurationEnvironment, "%q accepts at most %d entries.", "environment", MaxConfigurationEnvironment)
	case len(configuration.Secrets) > MaxConfigurationSecrets:
		return invalidConfiguration(FieldConfigurationSecrets, "%q accepts at most %d entries.", "secrets", MaxConfigurationSecrets)
	case len(configuration.Mounts) > MaxConfigurationMounts:
		return invalidConfiguration(FieldConfigurationMounts, "%q accepts at most %d entries.", "mounts", MaxConfigurationMounts)
	case len(configuration.Services) > MaxConfigurationServices:
		return invalidConfiguration(FieldConfigurationDesktop, "%q accepts at most %d entries.", "services", MaxConfigurationServices)
	case len(configuration.TerminalUIDs) > MaxConfigurationTerminalUIDs:
		return invalidConfiguration(FieldConfigurationTerminalUIDs, "%q accepts at most %d entries.", "terminal_uids", MaxConfigurationTerminalUIDs)
	}
	problems := make([]error, 0, 4)
	terminalUIDs := make(map[int64]struct{}, len(configuration.TerminalUIDs))
	for _, uid := range configuration.TerminalUIDs {
		if uid < 0 || uid > maxContainerID {
			problems = append(problems, invalidConfiguration(FieldConfigurationTerminalUIDs, "Terminal UID %d is outside the allowed range 0 to 2147483647.", uid))
		} else if _, exists := terminalUIDs[uid]; exists {
			problems = append(problems, invalidConfiguration(FieldConfigurationTerminalUIDs, "Terminal UID %d is listed more than once.", uid))
		}
		terminalUIDs[uid] = struct{}{}
	}
	for index, value := range configuration.Command {
		if len(value) == 0 || len(value) > 256 || hasControl(value) {
			problems = append(problems, invalidConfiguration(FieldConfigurationCommand, "Command argument %d must be 1 to 256 characters without control characters.", index+1))
		}
	}
	problems = append(problems, validateContainerUser(configuration)...)
	problems = append(problems, validateEnvironment(configuration)...)
	problems = append(problems, validateSecrets(configuration)...)
	problems = append(problems, validateMounts(configuration)...)
	problems = append(problems, validateServices(configuration)...)
	return domain.InvalidFields(problems...)
}

// validateContainerUser checks the optional container_user block.
func validateContainerUser(configuration domain.TemplateConfiguration) []error {
	user := configuration.ContainerUser
	if user == nil {
		return nil
	}
	problems := make([]error, 0, 2)
	for _, id := range []struct {
		field string
		key   string
		value *int64
	}{{FieldConfigurationUserUID, "uid", user.UID}, {FieldConfigurationUserGID, "gid", user.GID}} {
		switch {
		case id.value == nil:
			problems = append(problems, invalidConfiguration(id.field, "%q needs a %q.", "container_user", id.key))
		case *id.value < 0 || *id.value > maxContainerID:
			problems = append(problems, invalidConfiguration(id.field, "The %q %s must be between 0 and 2147483647.", "container_user", id.key))
		}
	}
	if user.Username != "" && user.Username != "{{cows.user.username}}" {
		problems = append(problems, invalidConfiguration(FieldConfigurationUser, "The %q username must be left out or set to %q.", "container_user", "{{cows.user.username}}"))
	}
	// Checked in a fixed order so a block with several problems always
	// reports them the same way.
	for _, text := range []struct {
		field string
		label string
		value string
	}{
		{FieldConfigurationUser, "username", user.Username},
		{FieldConfigurationUserDisplayName, "display name", user.Name},
		{FieldConfigurationUserHome, "home", user.Home},
		{FieldConfigurationUserShell, "shell", user.Shell},
	} {
		switch {
		case len(text.value) > 256 || hasControl(text.value) || strings.Contains(text.value, ":"):
			problems = append(problems, invalidConfiguration(text.field, "The %q %s must be at most 256 characters without control characters or a colon.", "container_user", text.label))
		case !validPlaceholders(text.value, configuration):
			problems = append(problems, invalidConfiguration(text.field, "The %q %s uses an unknown or unbalanced {{cows....}} placeholder.", "container_user", text.label))
		}
	}
	if user.Home != "" && (strings.HasPrefix(user.Home, "{{") || !validContainerPath(user.Home)) {
		problems = append(problems, invalidConfiguration(FieldConfigurationUserHome, "The %q home must be a literal absolute path other than \"/\", at most 256 characters, without \"..\".", "container_user"))
	}
	if user.Shell != "" && (strings.HasPrefix(user.Shell, "{{") || !validContainerPath(user.Shell)) {
		problems = append(problems, invalidConfiguration(FieldConfigurationUserShell, "The %q shell must be a literal absolute path other than \"/\", at most 256 characters, without \"..\".", "container_user"))
	}
	return problems
}

func validateEnvironment(configuration domain.TemplateConfiguration) []error {
	problems := make([]error, 0, 2)
	names := make(map[string]struct{}, len(configuration.Environment))
	for index, value := range configuration.Environment {
		nameField := ConfigurationEntryField(FieldConfigurationEnvironment, index, EntryName)
		valueField := ConfigurationEntryField(FieldConfigurationEnvironment, index, EntryValue)
		if !environmentNamePattern.MatchString(value.Name) {
			problems = append(problems, invalidConfiguration(nameField, "Environment name %q must start with a letter or underscore and use at most 128 letters, digits, or underscores.", value.Name))
		} else if _, exists := names[value.Name]; exists {
			problems = append(problems, invalidConfiguration(nameField, "Environment name %q is defined more than once.", value.Name))
		}
		names[value.Name] = struct{}{}
		if len(value.Value) > 4096 || hasControl(value.Value) {
			problems = append(problems, invalidConfiguration(valueField, "The value of environment entry %q must be at most 4096 characters without control characters.", value.Name))
		} else if !validPlaceholders(value.Value, configuration) {
			problems = append(problems, invalidConfiguration(valueField, "The value of environment entry %q uses an unknown or unbalanced {{cows....}} placeholder.", value.Name))
		}
	}
	return problems
}

func validateSecrets(configuration domain.TemplateConfiguration) []error {
	problems := make([]error, 0, 2)
	names := make(map[string]struct{}, len(configuration.Secrets))
	for index, value := range configuration.Secrets {
		nameField := ConfigurationEntryField(FieldConfigurationSecrets, index, EntryName)
		valueField := ConfigurationEntryField(FieldConfigurationSecrets, index, EntryValue)
		lengthField := ConfigurationEntryField(FieldConfigurationSecrets, index, EntryLength)
		if !configurationNamePattern.MatchString(value.Name) {
			problems = append(problems, invalidConfiguration(nameField, "Secret name %q must start with a lowercase letter and use at most 64 lowercase letters, digits, underscores, or dashes.", value.Name))
		} else if _, exists := names[value.Name]; exists {
			problems = append(problems, invalidConfiguration(nameField, "Secret name %q is defined more than once.", value.Name))
		}
		names[value.Name] = struct{}{}
		switch {
		case value.Length < 0 || value.Length > 256:
			problems = append(problems, invalidConfiguration(lengthField, "The length of secret %q must be between 0 and 256.", value.Name))
		case value.Generate && value.Length < 6:
			problems = append(problems, invalidConfiguration(lengthField, "Generated secret %q needs a length of at least 6.", value.Name))
		case !value.Generate && value.Length != 0:
			problems = append(problems, invalidConfiguration(lengthField, "Secret %q is not generated, so it must not set a length.", value.Name))
		}
		switch {
		case value.Generate && value.Value != "":
			problems = append(problems, invalidConfiguration(valueField, "Generated secret %q must not carry a value.", value.Name))
		case !value.Generate && (value.Value == "" || len(value.Value) > 4096 || hasControl(value.Value)):
			problems = append(problems, invalidConfiguration(valueField, "Secret %q needs a value of 1 to 4096 characters without control characters, or must be generated per workspace (\"generate\": true).", value.Name))
		}
	}
	return problems
}

func validateMounts(configuration domain.TemplateConfiguration) []error {
	problems := make([]error, 0, 2)
	names := make(map[string]struct{}, len(configuration.Mounts))
	managedNames := make(map[string]struct{}, len(configuration.Mounts))
	for index, value := range configuration.Mounts {
		field := func(attribute string) string {
			return ConfigurationEntryField(FieldConfigurationMounts, index, attribute)
		}
		mountType := normalizedMountType(value.Type)
		nameValid := configurationNamePattern.MatchString(value.Name)
		prefixValid := value.NamePrefix == "" || mountNamePartPattern.MatchString(value.NamePrefix)
		suffixValid := value.NameSuffix == "" || mountNamePartPattern.MatchString(value.NameSuffix)
		if !nameValid {
			problems = append(problems, invalidConfiguration(field(EntryName), "Mount name %q must start with a lowercase letter and use at most 64 lowercase letters, digits, underscores, or dashes.", value.Name))
		}
		if mountType != domain.TemplateMountVolume && mountType != domain.TemplateMountDirectory {
			problems = append(problems, invalidConfiguration(field(EntryType), "The type of mount %q must be %q or %q.", value.Name, domain.TemplateMountVolume, domain.TemplateMountDirectory))
		}
		if !validContainerPath(value.ContainerPath) {
			problems = append(problems, invalidConfiguration(field(EntryContainerPath), "The container path of mount %q must be an absolute path other than \"/\", at most 256 characters, without \"..\".", value.Name))
		} else if runtime.ProtectedSystemMount(value.ContainerPath) {
			problems = append(problems, invalidConfiguration(field(EntryContainerPath), "Mount %q must not replace system executables, libraries, or dynamic-loader configuration required for terminal cleanup.", value.Name))
		}
		if !prefixValid {
			problems = append(problems, invalidConfiguration(field(EntryNamePrefix), "The name prefix of mount %q must be at most 32 lowercase letters, digits, underscores, or dashes.", value.Name))
		}
		if !suffixValid {
			problems = append(problems, invalidConfiguration(field(EntryNameSuffix), "The name suffix of mount %q must be at most 32 lowercase letters, digits, underscores, or dashes.", value.Name))
		}
		if !nameValid || !prefixValid || !suffixValid {
			// The storage-name checks below compare names built from these
			// parts, so they only mean something once every part is valid.
			continue
		}
		managedName := managedMountName("workspace-id", value)
		_, duplicateName := names[value.Name]
		_, duplicateStorage := managedNames[managedName]
		switch {
		case len(managedName) > 128:
			problems = append(problems, invalidConfiguration(field(EntryName), "The generated storage name for mount %q is too long. Shorten its name, prefix, or suffix.", value.Name))
		case duplicateName:
			problems = append(problems, invalidConfiguration(field(EntryName), "Mount name %q is defined more than once.", value.Name))
		case duplicateStorage:
			problems = append(problems, invalidConfiguration(field(EntryName), "Mount %q generates the same storage name as another mount. Give it a different name, prefix, or suffix.", value.Name))
		}
		names[value.Name] = struct{}{}
		managedNames[managedName] = struct{}{}
	}
	return problems
}

// validateServices checks the services list. The only supported service is
// the desktop, which the form editor shows as a single block, so every
// rejection here lands on one of that block's controls.
func validateServices(configuration domain.TemplateConfiguration) []error {
	problems := make([]error, 0, 2)
	secrets := make(map[string]struct{}, len(configuration.Secrets))
	for _, secret := range configuration.Secrets {
		secrets[secret.Name] = struct{}{}
	}
	names := make(map[string]struct{}, len(configuration.Services))
	for _, value := range configuration.Services {
		if value.Name != "desktop" {
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktop, "The only supported service is %q, not %q.", "desktop", value.Name))
			continue
		}
		if _, exists := names[value.Name]; exists {
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktop, "Service name %q is defined more than once.", value.Name))
			continue
		}
		names[value.Name] = struct{}{}
		if value.Protocol != "tcp" {
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktop, "Service %q must use protocol %q.", value.Name, "tcp"))
		}
		if value.ContainerPort < 1 || value.ContainerPort > 65535 {
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktopContainerPort, "The container port of service %q must be between 1 and 65535.", value.Name))
		}
		startValid := value.HostPortStart >= 1024 && value.HostPortStart <= 65535
		if !startValid {
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktopHostPortStart, "The first host port of service %q must be between 1024 and 65535.", value.Name))
		}
		switch {
		case value.HostPortEnd < 1024 || value.HostPortEnd > 65535:
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktopHostPortEnd, "The last host port of service %q must be between 1024 and 65535.", value.Name))
		case startValid && value.HostPortEnd < value.HostPortStart:
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktopHostPortEnd, "The last host port of service %q must not be below its first host port.", value.Name))
		}
		if !configurationNamePattern.MatchString(value.PortPool) {
			problems = append(problems, invalidConfiguration(FieldConfigurationDesktopPortPool, "The port pool of service %q must start with a lowercase letter and use at most 64 lowercase letters, digits, underscores, or dashes.", value.Name))
		}
		if value.PasswordSecret != "" {
			if !configurationNamePattern.MatchString(value.PasswordSecret) {
				problems = append(problems, invalidConfiguration(FieldConfigurationDesktopPasswordSecret, "The password secret of service %q is not a valid secret name.", value.Name))
			} else if _, ok := secrets[value.PasswordSecret]; !ok {
				problems = append(problems, invalidConfiguration(FieldConfigurationDesktopPasswordSecret, "Service %q refers to secret %q, which is not defined under %q.", value.Name, value.PasswordSecret, "secrets"))
			}
		}
	}
	return problems
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
		case name == "cows.workspace_id", name == "cows.workspace_name", name == "cows.user.username", name == "cows.user.display_name":
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

func resolveConfiguration(configuration domain.TemplateConfiguration, user domain.User, workspaceID, workspaceName string, allocations []domain.PortAllocation, secrets map[string]string) (runtime.WorkspaceConfiguration, error) {
	ports := make(map[string]string, len(allocations))
	for _, allocation := range allocations {
		ports[allocation.ServiceName] = strconv.Itoa(allocation.HostPort)
	}
	values := map[string]string{
		"cows.workspace_id":      workspaceID,
		"cows.workspace_name":    workspaceName,
		"cows.user.username":     user.Username,
		"cows.user.display_name": user.DisplayName,
	}
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
	if configuration.ContainerUser != nil {
		containerUser, err := resolveContainerUser(*configuration.ContainerUser, user, values)
		if err != nil {
			return runtime.WorkspaceConfiguration{}, err
		}
		result.User = containerUser
	}
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
		result.Mounts = append(result.Mounts, runtime.Mount{Name: value.Name, Type: normalizedMountType(value.Type), ContainerPath: value.ContainerPath, ReadOnly: value.ReadOnly})
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

func resolveContainerUser(configuration domain.TemplateContainerUser, user domain.User, values map[string]string) (*runtime.ContainerUser, error) {
	if configuration.UID == nil || configuration.GID == nil {
		return nil, ErrInvalidTemplate
	}
	username := configuration.Username
	if username == "" {
		username = "{{cows.user.username}}"
	}
	resolvedUsername, err := resolveValue(username, values)
	if err != nil || !containerUsernamePattern.MatchString(resolvedUsername) {
		return nil, ErrInvalidTemplate
	}
	name := configuration.Name
	if name == "" {
		name = "{{cows.user.display_name}}"
	}
	resolvedName, err := resolveValue(name, values)
	if err != nil || resolvedName == "" || strings.Contains(resolvedName, ":") {
		return nil, ErrInvalidTemplate
	}
	home := configuration.Home
	if home == "" {
		home = "/home/" + resolvedUsername
	}
	resolvedHome, err := resolveValue(home, values)
	if err != nil || !validContainerPath(resolvedHome) {
		return nil, ErrInvalidTemplate
	}
	shell := configuration.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	resolvedShell, err := resolveValue(shell, values)
	if err != nil || !validContainerPath(resolvedShell) {
		return nil, ErrInvalidTemplate
	}
	uid, gid := *configuration.UID, *configuration.GID
	return &runtime.ContainerUser{
		Username:    resolvedUsername,
		UID:         uid,
		GID:         gid,
		Name:        resolvedName,
		Home:        resolvedHome,
		Shell:       resolvedShell,
		PasswdEntry: strings.Join([]string{resolvedUsername, "x", strconv.FormatInt(uid, 10), strconv.FormatInt(gid, 10), resolvedName, resolvedHome, resolvedShell}, ":"),
	}, nil
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
