package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cows-project/cows/internal/quota"
)

type Config struct {
	ListenAddr             string
	DatabasePath           string
	MountRoot              string
	MountArchiveRoot       string
	PodmanSocket           string
	HostStorageBytes       int64
	LogLevel               slog.Level
	ShutdownTimeout        time.Duration
	SessionLifetime        time.Duration
	CookieSecure           bool
	BootstrapAdminUsername string
	BootstrapAdminPassword string
	RegistrationEnabled    bool
	RegistrationGroups     []string
	RegistrationQuota      quota.Input
}

func Load(args []string) (Config, error) {
	return load(args, os.LookupEnv)
}

func load(args []string, lookup func(string) (string, bool)) (Config, error) {
	listenAddr := envOr(lookup, "COWS_LISTEN_ADDR", "127.0.0.1:8080")
	databasePath := envOr(lookup, "COWS_DATABASE_PATH", "./data/cows.db")
	mountRoot := envOr(lookup, "COWS_MOUNT_ROOT", "./data/cows-mounts")
	mountArchiveRoot := envOr(lookup, "COWS_MOUNT_ARCHIVE_ROOT", "./data/cows-mounts-archive")
	defaultPodmanSocket := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "podman", "podman.sock")
	podmanSocket := envOr(lookup, "COWS_PODMAN_SOCKET", defaultPodmanSocket)
	hostStorageValue := envOr(lookup, "COWS_HOST_STORAGE_BYTES", "0")
	logLevel := envOr(lookup, "COWS_LOG_LEVEL", "info")
	shutdownTimeout := envOr(lookup, "COWS_SHUTDOWN_TIMEOUT", "10s")
	sessionLifetime := envOr(lookup, "COWS_SESSION_LIFETIME", "8h")
	cookieSecureValue := envOr(lookup, "COWS_COOKIE_SECURE", "false")
	bootstrapUsername := envOr(lookup, "COWS_BOOTSTRAP_ADMIN_USERNAME", "")
	bootstrapPassword := envOr(lookup, "COWS_BOOTSTRAP_ADMIN_PASSWORD", "")
	registrationEnabledValue := envOr(lookup, "COWS_REGISTRATION_ENABLED", "false")
	registrationGroupsValue := envOr(lookup, "COWS_REGISTRATION_DEFAULT_GROUPS", "")
	registrationCPUValue := envOr(lookup, "COWS_REGISTRATION_DEFAULT_CPU_MILLIS", "2000")
	registrationMemoryValue := envOr(lookup, "COWS_REGISTRATION_DEFAULT_MEMORY_BYTES", "4294967296")
	registrationStorageValue := envOr(lookup, "COWS_REGISTRATION_DEFAULT_STORAGE_BYTES", "21474836480")
	registrationWorkspacesValue := envOr(lookup, "COWS_REGISTRATION_DEFAULT_MAX_WORKSPACES", "2")
	registrationRunningValue := envOr(lookup, "COWS_REGISTRATION_DEFAULT_MAX_RUNNING_WORKSPACES", "1")
	cookieSecure, err := strconv.ParseBool(cookieSecureValue)
	if err != nil {
		return Config{}, fmt.Errorf("cookie secure must be true or false: %q", cookieSecureValue)
	}
	registrationEnabled, err := strconv.ParseBool(registrationEnabledValue)
	if err != nil {
		return Config{}, fmt.Errorf("registration enabled must be true or false: %q", registrationEnabledValue)
	}

	flags := flag.NewFlagSet("cows", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&listenAddr, "listen-addr", listenAddr, "HTTP listen address")
	flags.StringVar(&databasePath, "database-path", databasePath, "SQLite database path")
	flags.StringVar(&mountRoot, "mount-root", mountRoot, "root directory for COWS-managed directory mounts")
	flags.StringVar(&mountArchiveRoot, "mount-archive-root", mountArchiveRoot, "root directory for archived COWS-managed workspace data")
	flags.StringVar(&podmanSocket, "podman-socket", podmanSocket, "rootless Podman Unix socket path")
	flags.StringVar(&hostStorageValue, "host-storage-bytes", hostStorageValue, "configured allocatable host storage in bytes; zero means unknown")
	flags.StringVar(&logLevel, "log-level", logLevel, "log level: debug, info, warn, or error")
	flags.StringVar(&shutdownTimeout, "shutdown-timeout", shutdownTimeout, "graceful shutdown timeout")
	flags.StringVar(&sessionLifetime, "session-lifetime", sessionLifetime, "authenticated session lifetime")
	flags.BoolVar(&cookieSecure, "cookie-secure", cookieSecure, "mark browser cookies Secure")
	flags.StringVar(&bootstrapUsername, "bootstrap-admin-username", bootstrapUsername, "initial administrator username")
	flags.StringVar(&bootstrapPassword, "bootstrap-admin-password", bootstrapPassword, "initial administrator password")
	flags.BoolVar(&registrationEnabled, "registration-enabled", registrationEnabled, "enable public local-account registration")
	flags.StringVar(&registrationGroupsValue, "registration-default-groups", registrationGroupsValue, "comma-separated default group names for self-registered users")
	flags.StringVar(&registrationCPUValue, "registration-default-cpu-millis", registrationCPUValue, "default registered-user CPU quota")
	flags.StringVar(&registrationMemoryValue, "registration-default-memory-bytes", registrationMemoryValue, "default registered-user memory quota")
	flags.StringVar(&registrationStorageValue, "registration-default-storage-bytes", registrationStorageValue, "default registered-user storage quota")
	flags.StringVar(&registrationWorkspacesValue, "registration-default-max-workspaces", registrationWorkspacesValue, "default registered-user workspace quota")
	flags.StringVar(&registrationRunningValue, "registration-default-max-running-workspaces", registrationRunningValue, "default registered-user running-workspace quota")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}

	if strings.TrimSpace(listenAddr) == "" {
		return Config{}, errors.New("listen address must not be empty")
	}
	if strings.TrimSpace(databasePath) == "" {
		return Config{}, errors.New("database path must not be empty")
	}
	if strings.TrimSpace(mountRoot) == "" {
		return Config{}, errors.New("mount root must not be empty")
	}
	if strings.TrimSpace(mountArchiveRoot) == "" {
		return Config{}, errors.New("mount archive root must not be empty")
	}
	mountRootAbsolute, err := filepath.Abs(mountRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve mount root: %w", err)
	}
	mountArchiveRootAbsolute, err := filepath.Abs(mountArchiveRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve mount archive root: %w", err)
	}
	if mountRootAbsolute == mountArchiveRootAbsolute {
		return Config{}, errors.New("mount root and mount archive root must be different directories")
	}
	if pathContains(mountRootAbsolute, mountArchiveRootAbsolute) || pathContains(mountArchiveRootAbsolute, mountRootAbsolute) {
		return Config{}, errors.New("mount root and mount archive root must not contain one another")
	}
	if strings.TrimSpace(podmanSocket) == "" {
		return Config{}, errors.New("Podman socket path must not be empty")
	}
	hostStorageBytes, err := strconv.ParseInt(hostStorageValue, 10, 64)
	if err != nil || hostStorageBytes < 0 {
		return Config{}, fmt.Errorf("host storage bytes must be zero or positive: %q", hostStorageValue)
	}

	level, err := parseLogLevel(logLevel)
	if err != nil {
		return Config{}, err
	}
	timeout, err := time.ParseDuration(shutdownTimeout)
	if err != nil || timeout <= 0 {
		return Config{}, fmt.Errorf("shutdown timeout must be a positive duration: %q", shutdownTimeout)
	}
	sessionDuration, err := time.ParseDuration(sessionLifetime)
	if err != nil || sessionDuration <= 0 {
		return Config{}, fmt.Errorf("session lifetime must be a positive duration: %q", sessionLifetime)
	}
	if (bootstrapUsername == "") != (bootstrapPassword == "") {
		return Config{}, errors.New("bootstrap administrator username and password must be provided together")
	}
	registrationQuota, err := parseRegistrationQuota(registrationCPUValue, registrationMemoryValue, registrationStorageValue, registrationWorkspacesValue, registrationRunningValue)
	if err != nil {
		return Config{}, err
	}
	if err := quota.ValidateInput(registrationQuota); err != nil {
		return Config{}, fmt.Errorf("registration default quota: %w", err)
	}
	registrationGroups, err := parseNames(registrationGroupsValue)
	if err != nil {
		return Config{}, fmt.Errorf("registration default groups: %w", err)
	}

	return Config{
		ListenAddr:             listenAddr,
		DatabasePath:           databasePath,
		MountRoot:              mountRoot,
		MountArchiveRoot:       mountArchiveRoot,
		PodmanSocket:           podmanSocket,
		HostStorageBytes:       hostStorageBytes,
		LogLevel:               level,
		ShutdownTimeout:        timeout,
		SessionLifetime:        sessionDuration,
		CookieSecure:           cookieSecure,
		BootstrapAdminUsername: bootstrapUsername,
		BootstrapAdminPassword: bootstrapPassword,
		RegistrationEnabled:    registrationEnabled,
		RegistrationGroups:     registrationGroups,
		RegistrationQuota:      registrationQuota,
	}, nil
}

func parseRegistrationQuota(values ...string) (quota.Input, error) {
	parsed := make([]int64, len(values))
	for index, value := range values {
		integer, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return quota.Input{}, fmt.Errorf("registration default quota value is invalid: %q", value)
		}
		parsed[index] = integer
	}
	return quota.Input{
		MaxCPUMillis:         parsed[0],
		MaxMemoryBytes:       parsed[1],
		MaxStorageBytes:      parsed[2],
		MaxWorkspaces:        parsed[3],
		MaxRunningWorkspaces: parsed[4],
	}, nil
}

func parseNames(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, errors.New("group names must not be empty")
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("group %q is repeated", name)
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	return result, nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func envOr(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && value != "" {
		return value
	}
	return fallback
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: use debug, info, warn, or error", value)
	}
}
