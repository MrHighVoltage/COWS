package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/config"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/quota"
	"github.com/cows-project/cows/internal/repository/sqlite"
	"github.com/cows-project/cows/internal/runtime/docker"
	"github.com/cows-project/cows/internal/web"
	"github.com/cows-project/cows/internal/workspace"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "cows: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	store := sqlite.New(db)

	authService, err := auth.New(store, cfg.SessionLifetime)
	if err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}
	if cfg.BootstrapAdminUsername != "" {
		created, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{
			Username: cfg.BootstrapAdminUsername,
			Password: cfg.BootstrapAdminPassword,
		})
		if err != nil {
			return fmt.Errorf("bootstrap administrator: %w", err)
		}
		if created {
			logger.Info("bootstrap administrator created", "username", cfg.BootstrapAdminUsername)
		}
	}

	dockerRuntime, err := docker.New(cfg.DockerSocket)
	if err != nil {
		return fmt.Errorf("initialize Docker runtime: %w", err)
	}
	quotaService := quota.New(store)
	if _, err := quotaService.EnsureHostSettings(ctx, quota.HostSettingsInput{HostStorageBytes: cfg.HostStorageBytes}); err != nil {
		return fmt.Errorf("initialize host settings: %w", err)
	}
	scheduler := quota.NewScheduler(store, dockerRuntime)
	templateService := workspace.NewWithRuntime(store, dockerRuntime, scheduler)
	webServer, err := web.New(db, authService, templateService, quotaService, dockerRuntime, web.Options{CookieSecure: cfg.CookieSecure, SessionLifetime: cfg.SessionLifetime})
	if err != nil {
		return fmt.Errorf("initialize web server: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           webServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("COWS server started", "address", cfg.ListenAddr, "database", cfg.DatabasePath)
		serverErrors <- server.ListenAndServe()
	}()
	go runTimeoutLoop(ctx, templateService, logger)

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		logger.Info("COWS server shutting down", "reason", ctx.Err())
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	}
}

func runTimeoutLoop(ctx context.Context, service *workspace.Service, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	processWorkspaceRuntime(ctx, service, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processWorkspaceRuntime(ctx, service, logger)
		}
	}
}

func processWorkspaceRuntime(ctx context.Context, service *workspace.Service, logger *slog.Logger) {
	if err := service.Reconcile(ctx); err != nil {
		if !errors.Is(err, workspace.ErrRuntimeUnavailable) {
			logger.Error("workspace reconciliation failed", "error", err)
		}
		return
	}
	if err := service.RunTimeouts(ctx); err != nil && !errors.Is(err, workspace.ErrRuntimeUnavailable) {
		logger.Error("workspace timeout processing failed", "error", err)
	}
}
