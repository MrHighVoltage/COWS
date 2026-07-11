package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr      string
	DatabasePath    string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

func Load(args []string) (Config, error) {
	return load(args, os.LookupEnv)
}

func load(args []string, lookup func(string) (string, bool)) (Config, error) {
	listenAddr := envOr(lookup, "COWS_LISTEN_ADDR", "127.0.0.1:8080")
	databasePath := envOr(lookup, "COWS_DATABASE_PATH", "./data/cows.db")
	logLevel := envOr(lookup, "COWS_LOG_LEVEL", "info")
	shutdownTimeout := envOr(lookup, "COWS_SHUTDOWN_TIMEOUT", "10s")

	flags := flag.NewFlagSet("cows", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&listenAddr, "listen-addr", listenAddr, "HTTP listen address")
	flags.StringVar(&databasePath, "database-path", databasePath, "SQLite database path")
	flags.StringVar(&logLevel, "log-level", logLevel, "log level: debug, info, warn, or error")
	flags.StringVar(&shutdownTimeout, "shutdown-timeout", shutdownTimeout, "graceful shutdown timeout")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}

	if strings.TrimSpace(listenAddr) == "" {
		return Config{}, errors.New("listen address must not be empty")
	}
	if strings.TrimSpace(databasePath) == "" {
		return Config{}, errors.New("database path must not be empty")
	}

	level, err := parseLogLevel(logLevel)
	if err != nil {
		return Config{}, err
	}
	timeout, err := time.ParseDuration(shutdownTimeout)
	if err != nil || timeout <= 0 {
		return Config{}, fmt.Errorf("shutdown timeout must be a positive duration: %q", shutdownTimeout)
	}

	return Config{
		ListenAddr:      listenAddr,
		DatabasePath:    databasePath,
		LogLevel:        level,
		ShutdownTimeout: timeout,
	}, nil
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
