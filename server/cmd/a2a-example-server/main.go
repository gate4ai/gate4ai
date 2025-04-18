package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gate4ai/gate4ai/server"
	"github.com/gate4ai/gate4ai/server/a2a"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/gate4ai/shared/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	// --- Basic Setup ---
	loggerConfig := zap.NewProductionConfig()
	loggerConfig.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, _ := loggerConfig.Build()
	defer logger.Sync()

	listenAddr := flag.String("listen", ":41241", "Address and port to listen on")
	// Agent URL is now determined dynamically based on listen address
	flag.Parse()

	// --- Configuration ---
	// Using internal config for simplicity, but could use YAML or DB config.
	cfg := config.NewInternalConfig()
	cfg.LogLevelValue = "debug"                                 // Set log level
	cfg.AuthorizationTypeValue = config.NotAuthorizedEverywhere // Allow all requests for example

	// --- A2A Capability Setup ---
	taskStore := a2a.NewInMemoryTaskStore()
	// Pass the logger to the handler via a closure
	handlerWithLogger := func(ctx context.Context, task *a2aSchema.Task, updates chan<- a2a.A2AYieldUpdate, logger *zap.Logger) error {
		return a2a.ScenarioBasedA2AHandler(ctx, task, updates, logger)
	}

	// --- Server Setup ---
	// Use server.Start which handles manager and transport creation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger.Info("Starting A2A Example Server", zap.String("address", *listenAddr))

	// Use server builder options
	serverOptions := []server.ServerOption{
		server.WithListenAddr(*listenAddr),
		server.WithA2ACapability(taskStore, handlerWithLogger), // Register the A2A capability
		// Add other options if needed (e.g., MCP capabilities if this server supports both)
	}

	// Start the server
	errChan, startErr := server.Start(ctx, logger, cfg, serverOptions...)
	if startErr != nil {
		logger.Fatal("Failed to start server", zap.Error(startErr))
	}

	// --- Graceful Shutdown ---
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-signalCh:
		logger.Info("Received shutdown signal", zap.String("signal", sig.String()))
		cancel() // Trigger context cancellation for server.Start's cleanup goroutine
	case err := <-errChan:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Server listener error", zap.Error(err))
		} else {
			logger.Info("Server listener closed gracefully.")
		}
		cancel() // Ensure context is cancelled if listener fails/closes
	case <-ctx.Done(): // Handle potential external cancellation
		logger.Info("Server context cancelled externally")
	}

	// Wait briefly for graceful shutdown initiated by context cancellation in server.Start
	shutdownGracePeriod := 5 * time.Second
	shutdownTimer := time.NewTimer(shutdownGracePeriod)
	select {
	case <-shutdownTimer.C:
		logger.Warn("Shutdown grace period timed out.")
	// We rely on server.Start's goroutine to finish closing resources.
	// No explicit server.Shutdown() call needed here as Start manages it via context.
	case <-func() chan struct{} { // Effectively wait for shutdown completion if errChan is closed
		done := make(chan struct{})
		go func() {
			_, ok := <-errChan // Check if channel is closed (signaling listener stopped)
			if !ok {
				close(done)
			}
		}()
		return done
	}():
		logger.Info("Server shutdown seems complete.")
	}

	logger.Info("Server stopped")
}
