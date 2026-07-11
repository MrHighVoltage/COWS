package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr             string
	DatabasePath           string
	LogLevel               slog.Level
	ShutdownTimeout        time.Duration
	SessionLifetime        time.Duration
	CookieSecure           bool
	BootstrapAdminUsername string
	BootstrapAdminPassword string
}

func Load(args []string) (Config, error) {
	return load(args, os.LookupEnv)
}

func load(args []string, lookup func(string) (string, bool)) (Config, error) {
	listenAddr := envOr(lookup, "COWS_LISTEN_ADDR", "127.0.0.1:8080")
	databasePath := envOr(lookup, "COWS_DATABASE_PATH", "./data/cows.db")
	logLevel := envOr(lookup, "COWS_LOG_LEVEL", "info")
	shutdownTimeout := envOr(lookup, "COWS_SHUTDOWN_TIMEOUT", "10s")
	sessionLifetime := envOr(lookup, "COWS_SESSION_LIFETIME", "8h")
	cookieSecureValue := envOr(lookup, "COWS_COOKIE_SECURE", "false")
	bootstrapUsername := envOr(lookup, "COWS_BOOTSTRAP_ADMIN_USERNAME", "")
	bootstrapPassword := envOr(lookup, "COWS_BOOTSTRAP_ADMIN_PASSWORD", "")
	cookieSecure, err := strconv.ParseBool(cookieSecureValue)
	if err != nil {
		return Config{}, fmt.Errorf("cookie secure must be true or false: %q", cookieSecureValue)
	}

	flags := flag.NewFlagSet("cows", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&listenAddr, "listen-addr", listenAddr, "HTTP listen address")
	flags.StringVar(&databasePath, "database-path", databasePath, "SQLite database path")
	flags.StringVar(&logLevel, "log-level", logLevel, "log level: debug, info, warn, or error")
	flags.StringVar(&shutdownTimeout, "shutdown-timeout", shutdownTimeout, "graceful shutdown timeout")
	flags.StringVar(&sessionLifetime, "session-lifetime", sessionLifetime, "authenticated session lifetime")
	flags.BoolVar(&cookieSecure, "cookie-secure", cookieSecure, "mark browser cookies Secure")
	flags.StringVar(&bootstrapUsername, "bootstrap-admin-username", bootstrapUsername, "initial administrator username")
	flags.StringVar(&bootstrapPassword, "bootstrap-admin-password", bootstrapPassword, "initial administrator password")
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
	sessionDuration, err := time.ParseDuration(sessionLifetime)
	if err != nil || sessionDuration <= 0 {
		return Config{}, fmt.Errorf("session lifetime must be a positive duration: %q", sessionLifetime)
	}
	if (bootstrapUsername == "") != (bootstrapPassword == "") {
		return Config{}, errors.New("bootstrap administrator username and password must be provided together")
	}

	return Config{
		ListenAddr:             listenAddr,
		DatabasePath:           databasePath,
		LogLevel:               level,
		ShutdownTimeout:        timeout,
		SessionLifetime:        sessionDuration,
		CookieSecure:           cookieSecure,
		BootstrapAdminUsername: bootstrapUsername,
		BootstrapAdminPassword: bootstrapPassword,
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
