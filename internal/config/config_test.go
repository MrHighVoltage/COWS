package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(nil, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" || cfg.DatabasePath != "./data/cows.db" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected parsed defaults: %+v", cfg)
	}
}

func TestLoadEnvironmentAndFlags(t *testing.T) {
	env := map[string]string{
		"COWS_LISTEN_ADDR":      "127.0.0.1:9090",
		"COWS_DATABASE_PATH":    "/tmp/cows.db",
		"COWS_LOG_LEVEL":        "debug",
		"COWS_SHUTDOWN_TIMEOUT": "2s",
	}
	cfg, err := load([]string{"-listen-addr", "127.0.0.1:7070"}, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load configured values: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7070" || cfg.DatabasePath != "/tmp/cows.db" || cfg.LogLevel != slog.LevelDebug || cfg.ShutdownTimeout != 2*time.Second {
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
		{name: "bad log level", args: []string{"-log-level", "trace"}},
		{name: "bad timeout", args: []string{"-shutdown-timeout", "0s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := load(tt.args, func(string) (string, bool) { return "", false }); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
