package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(nil, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	defaultSocket := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "podman", "podman.sock")
	if cfg.ListenAddr != "127.0.0.1:8080" || cfg.DatabasePath != "./data/cows.db" || cfg.MountArchiveRoot != "./data/cows-mounts-archive" || cfg.PodmanSocket != defaultSocket || cfg.HostStorageBytes != 0 || cfg.HostCPUOverbookingFactor != 1 || cfg.HostMemoryOverbookingFactor != 1 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.ShutdownTimeout != 10*time.Second || cfg.SessionLifetime != 8*time.Hour || cfg.CookieSecure {
		t.Fatalf("unexpected parsed defaults: %+v", cfg)
	}
	if cfg.RegistrationEnabled || len(cfg.RegistrationGroups) != 0 || cfg.RegistrationQuota.MaxCPUMillis != 2000 || cfg.RegistrationQuota.MaxMemoryBytes != 4<<30 || cfg.RegistrationQuota.MaxStorageBytes != 20<<30 || cfg.RegistrationQuota.MaxWorkspaces != 2 || cfg.RegistrationQuota.MaxRunningWorkspaces != 1 {
		t.Fatalf("unexpected registration defaults: %+v", cfg)
	}
	if cfg.EmailEnabled || cfg.SMTPPort != 587 || cfg.SMTPRequireTLS != true || cfg.EmailWarningLeadTime != 24*time.Hour || cfg.EmailRetryInterval != 15*time.Minute {
		t.Fatalf("unexpected email defaults: %+v", cfg)
	}
}

func TestLoadEnvironmentAndFlags(t *testing.T) {
	env := map[string]string{
		"COWS_LISTEN_ADDR":                                 "127.0.0.1:9090",
		"COWS_HOST_CPU_OVERBOOKING_FACTOR":                 "2.5",
		"COWS_HOST_MEMORY_OVERBOOKING_FACTOR":              "0.8",
		"COWS_DATABASE_PATH":                               "/tmp/cows.db",
		"COWS_LOG_LEVEL":                                   "debug",
		"COWS_SHUTDOWN_TIMEOUT":                            "2s",
		"COWS_MOUNT_ARCHIVE_ROOT":                          "/srv/cows-archive",
		"COWS_REGISTRATION_ENABLED":                        "true",
		"COWS_REGISTRATION_DEFAULT_GROUPS":                 "research, teaching",
		"COWS_REGISTRATION_DEFAULT_CPU_MILLIS":             "3000",
		"COWS_REGISTRATION_DEFAULT_MEMORY_BYTES":           "8589934592",
		"COWS_REGISTRATION_DEFAULT_STORAGE_BYTES":          "32212254720",
		"COWS_REGISTRATION_DEFAULT_MAX_WORKSPACES":         "4",
		"COWS_REGISTRATION_DEFAULT_MAX_RUNNING_WORKSPACES": "2",
		"COWS_EMAIL_ENABLED":                               "true",
		"COWS_SMTP_HOST":                                   "smtp.example.test",
		"COWS_SMTP_PORT":                                   "2525",
		"COWS_SMTP_FROM":                                   "cows@example.test",
		"COWS_SMTP_USERNAME":                               "cows",
		"COWS_SMTP_PASSWORD":                               "secret",
		"COWS_EMAIL_WARNING_LEAD_TIME":                     "2h",
		"COWS_EMAIL_RETRY_INTERVAL":                        "10m",
	}
	cfg, err := load([]string{"-listen-addr", "127.0.0.1:7070"}, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load configured values: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7070" || cfg.DatabasePath != "/tmp/cows.db" || cfg.MountArchiveRoot != "/srv/cows-archive" || cfg.HostCPUOverbookingFactor != 2.5 || cfg.HostMemoryOverbookingFactor != 0.8 || cfg.LogLevel != slog.LevelDebug || cfg.ShutdownTimeout != 2*time.Second || !cfg.RegistrationEnabled || len(cfg.RegistrationGroups) != 2 || cfg.RegistrationQuota.MaxMemoryBytes != 8<<30 || cfg.RegistrationQuota.MaxRunningWorkspaces != 2 || !cfg.EmailEnabled || cfg.SMTPPort != 2525 || cfg.SMTPFrom != "cows@example.test" || cfg.EmailWarningLeadTime != 2*time.Hour || cfg.EmailRetryInterval != 10*time.Minute {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "empty address", args: []string{"-listen-addr", " "}},
		{name: "empty database", args: []string{"-database-path", ""}},
		{name: "same mount roots", args: []string{"-mount-root", "/srv/cows-mounts", "-mount-archive-root", "/srv/cows-mounts"}},
		{name: "nested mount roots", args: []string{"-mount-root", "/srv/cows", "-mount-archive-root", "/srv/cows/archive"}},
		{name: "empty Podman socket", args: []string{"-podman-socket", " "}},
		{name: "negative host storage", args: []string{"-host-storage-bytes", "-1"}},
		{name: "invalid host CPU overbooking factor", args: []string{"-host-cpu-overbooking-factor", "0.05"}},
		{name: "invalid host memory overbooking factor", args: []string{"-host-memory-overbooking-factor", "1000.1"}},
		{name: "bad log level", args: []string{"-log-level", "trace"}},
		{name: "bad timeout", args: []string{"-shutdown-timeout", "0s"}},
		{name: "bad session lifetime", args: []string{"-session-lifetime", "0s"}},
		{name: "incomplete bootstrap", args: []string{"-bootstrap-admin-username", "admin"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := load(tt.args, func(string) (string, bool) { return "", false }); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
