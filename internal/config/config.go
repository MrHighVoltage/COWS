package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cows-project/cows/internal/quota"
)

type Config struct {
	ListenAddr                  string
	DatabasePath                string
	MountRoot                   string
	MountArchiveRoot            string
	PodmanSocket                string
	HostStorageBytes            int64
	HostCPUOverbookingFactor    float64
	HostMemoryOverbookingFactor float64
	LogLevel                    slog.Level
	ShutdownTimeout             time.Duration
	SessionLifetime             time.Duration
	CookieSecure                bool
	BootstrapAdminUsername      string
	BootstrapAdminPassword      string
	RegistrationEnabled         bool
	RegistrationGroups          []string
	RegistrationQuota           quota.Input
	EmailEnabled                bool
	SMTPHost                    string
	SMTPPort                    int
	SMTPFrom                    string
	SMTPUsername                string
	SMTPPassword                string
	SMTPRequireTLS              bool
	EmailWarningLeadTime        time.Duration
	EmailRetryInterval          time.Duration
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
	hostCPUOverbookingValue := envOr(lookup, "COWS_HOST_CPU_OVERBOOKING_FACTOR", "1")
	hostMemoryOverbookingValue := envOr(lookup, "COWS_HOST_MEMORY_OVERBOOKING_FACTOR", "1")
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
	emailEnabledValue := envOr(lookup, "COWS_EMAIL_ENABLED", "false")
	smtpHost := envOr(lookup, "COWS_SMTP_HOST", "")
	smtpPortValue := envOr(lookup, "COWS_SMTP_PORT", "587")
	smtpFrom := envOr(lookup, "COWS_SMTP_FROM", "")
	smtpUsername := envOr(lookup, "COWS_SMTP_USERNAME", "")
	smtpPassword := envOr(lookup, "COWS_SMTP_PASSWORD", "")
	smtpRequireTLSValue := envOr(lookup, "COWS_SMTP_REQUIRE_TLS", "true")
	emailWarningLeadValue := envOr(lookup, "COWS_EMAIL_WARNING_LEAD_TIME", "24h")
	emailRetryIntervalValue := envOr(lookup, "COWS_EMAIL_RETRY_INTERVAL", "15m")
	cookieSecure, err := strconv.ParseBool(cookieSecureValue)
	if err != nil {
		return Config{}, fmt.Errorf("cookie secure must be true or false: %q", cookieSecureValue)
	}
	registrationEnabled, err := strconv.ParseBool(registrationEnabledValue)
	if err != nil {
		return Config{}, fmt.Errorf("registration enabled must be true or false: %q", registrationEnabledValue)
	}
	emailEnabled, err := strconv.ParseBool(emailEnabledValue)
	if err != nil {
		return Config{}, fmt.Errorf("email enabled must be true or false: %q", emailEnabledValue)
	}
	smtpRequireTLS, err := strconv.ParseBool(smtpRequireTLSValue)
	if err != nil {
		return Config{}, fmt.Errorf("SMTP require TLS must be true or false: %q", smtpRequireTLSValue)
	}

	flags := flag.NewFlagSet("cows", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&listenAddr, "listen-addr", listenAddr, "HTTP listen address")
	flags.StringVar(&databasePath, "database-path", databasePath, "SQLite database path")
	flags.StringVar(&mountRoot, "mount-root", mountRoot, "root directory for COWS-managed directory mounts")
	flags.StringVar(&mountArchiveRoot, "mount-archive-root", mountArchiveRoot, "root directory for archived COWS-managed workspace data")
	flags.StringVar(&podmanSocket, "podman-socket", podmanSocket, "rootless Podman Unix socket path")
	flags.StringVar(&hostStorageValue, "host-storage-bytes", hostStorageValue, "configured allocatable host storage in bytes; zero means unknown")
	flags.StringVar(&hostCPUOverbookingValue, "host-cpu-overbooking-factor", hostCPUOverbookingValue, "CPU host overbooking factor")
	flags.StringVar(&hostMemoryOverbookingValue, "host-memory-overbooking-factor", hostMemoryOverbookingValue, "memory host overbooking factor")
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
	flags.BoolVar(&emailEnabled, "email-enabled", emailEnabled, "enable lifecycle email notifications")
	flags.StringVar(&smtpHost, "smtp-host", smtpHost, "SMTP server hostname")
	flags.StringVar(&smtpPortValue, "smtp-port", smtpPortValue, "SMTP server port")
	flags.StringVar(&smtpFrom, "smtp-from", smtpFrom, "SMTP sender address")
	flags.StringVar(&smtpUsername, "smtp-username", smtpUsername, "SMTP username")
	flags.StringVar(&smtpPassword, "smtp-password", smtpPassword, "SMTP password")
	flags.BoolVar(&smtpRequireTLS, "smtp-require-tls", smtpRequireTLS, "require STARTTLS for email delivery")
	flags.StringVar(&emailWarningLeadValue, "email-warning-lead-time", emailWarningLeadValue, "lead time for lifecycle warning emails")
	flags.StringVar(&emailRetryIntervalValue, "email-retry-interval", emailRetryIntervalValue, "retry interval for failed warning emails")
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
	hostCPUOverbookingFactor, err := strconv.ParseFloat(strings.TrimSpace(hostCPUOverbookingValue), 64)
	if err != nil || math.IsNaN(hostCPUOverbookingFactor) || math.IsInf(hostCPUOverbookingFactor, 0) || hostCPUOverbookingFactor < quota.MinOverbookingFactor || hostCPUOverbookingFactor > quota.MaxOverbookingFactor {
		return Config{}, fmt.Errorf("host CPU overbooking factor must be between %.1f and %d: %q", quota.MinOverbookingFactor, quota.MaxOverbookingFactor, hostCPUOverbookingValue)
	}
	hostMemoryOverbookingFactor, err := strconv.ParseFloat(strings.TrimSpace(hostMemoryOverbookingValue), 64)
	if err != nil || math.IsNaN(hostMemoryOverbookingFactor) || math.IsInf(hostMemoryOverbookingFactor, 0) || hostMemoryOverbookingFactor < quota.MinOverbookingFactor || hostMemoryOverbookingFactor > quota.MaxOverbookingFactor {
		return Config{}, fmt.Errorf("host memory overbooking factor must be between %.1f and %d: %q", quota.MinOverbookingFactor, quota.MaxOverbookingFactor, hostMemoryOverbookingValue)
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
	smtpPort, err := strconv.Atoi(strings.TrimSpace(smtpPortValue))
	if err != nil || smtpPort < 1 || smtpPort > 65535 {
		return Config{}, fmt.Errorf("SMTP port must be between 1 and 65535: %q", smtpPortValue)
	}
	emailWarningLeadTime, err := time.ParseDuration(emailWarningLeadValue)
	if err != nil || emailWarningLeadTime <= 0 {
		return Config{}, fmt.Errorf("email warning lead time must be positive: %q", emailWarningLeadValue)
	}
	emailRetryInterval, err := time.ParseDuration(emailRetryIntervalValue)
	if err != nil || emailRetryInterval <= 0 {
		return Config{}, fmt.Errorf("email retry interval must be positive: %q", emailRetryIntervalValue)
	}
	if emailEnabled {
		if strings.TrimSpace(smtpHost) == "" || strings.TrimSpace(smtpFrom) == "" {
			return Config{}, errors.New("SMTP host and sender are required when email is enabled")
		}
		parsedFrom, parseErr := mail.ParseAddress(strings.TrimSpace(smtpFrom))
		if parseErr != nil || parsedFrom.Address != strings.TrimSpace(smtpFrom) {
			return Config{}, errors.New("SMTP sender address is invalid")
		}
	}
	if (smtpUsername == "") != (smtpPassword == "") {
		return Config{}, errors.New("SMTP username and password must be provided together")
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
		ListenAddr:                  listenAddr,
		DatabasePath:                databasePath,
		MountRoot:                   mountRoot,
		MountArchiveRoot:            mountArchiveRoot,
		PodmanSocket:                podmanSocket,
		HostStorageBytes:            hostStorageBytes,
		HostCPUOverbookingFactor:    hostCPUOverbookingFactor,
		HostMemoryOverbookingFactor: hostMemoryOverbookingFactor,
		LogLevel:                    level,
		ShutdownTimeout:             timeout,
		SessionLifetime:             sessionDuration,
		CookieSecure:                cookieSecure,
		BootstrapAdminUsername:      bootstrapUsername,
		BootstrapAdminPassword:      bootstrapPassword,
		RegistrationEnabled:         registrationEnabled,
		RegistrationGroups:          registrationGroups,
		RegistrationQuota:           registrationQuota,
		EmailEnabled:                emailEnabled,
		SMTPHost:                    strings.TrimSpace(smtpHost),
		SMTPPort:                    smtpPort,
		SMTPFrom:                    strings.TrimSpace(smtpFrom),
		SMTPUsername:                smtpUsername,
		SMTPPassword:                smtpPassword,
		SMTPRequireTLS:              smtpRequireTLS,
		EmailWarningLeadTime:        emailWarningLeadTime,
		EmailRetryInterval:          emailRetryInterval,
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
