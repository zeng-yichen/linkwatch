package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"linkwatch/internal/api"
	"linkwatch/internal/checker"
	"linkwatch/internal/config"
	"linkwatch/internal/storage/sqlite"
)

func main() {
	// The main function is the entry point of the application.
	// It's responsible for initializing components, starting the server,
	// and handling graceful shutdown.
	if err := run(); err != nil {
		log.Fatalf("application failed: %v", err)
	}
	log.Println("application shut down gracefully")
}

func run() error {
	// Load application configuration from environment variables.
	cfg := config.Load()

	// Create a context that is canceled on OS signals like SIGINT or SIGTERM.
	// This is the foundation for graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Initialize the SQLite storage layer.
	log.Println("initializing SQLite database connection...")
	store, err := sqlite.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to initialize sqlite storage: %w", err)
	}
	defer store.Close()
	log.Println("database connection successful")

	// Initialize the background checker and the API server.
	checkerSvc := checker.New(store, cfg.CheckInterval, cfg.MaxConcurrency, cfg.HTTPTimeout)
	server := api.NewServer(cfg.HTTPPort, store)

	// Start the services.
	checkerSvc.Start()
	server.Start()

	log.Println("application is running...")

	// Block here until the context is canceled (e.g., by pressing Ctrl+C).
	<-ctx.Done()

	// --- Graceful shutdown logic ---
	log.Println("shutdown signal received, starting graceful shutdown...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer shutdownCancel()

	// Stop the checker first to prevent new checks from starting.
	checkerSvc.Stop()

	// Then, shut down the HTTP server, allowing in-flight requests to finish.
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server shutdown error: %w", err)
	}

	return nil
}
