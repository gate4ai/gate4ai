package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gate4ai/mcp/server/extra"
	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/server/mcp/capability"
	"github.com/gate4ai/mcp/server/mcp/validators"
	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
)

// StartServer starts the MCP SSE server with the provided options
// It now returns the capabilities for customization by callers like startExample.
func StartServer(ctx context.Context, logger *zap.Logger, cfg config.IConfig, overwriteListenAddr string) (
	*capability.ToolsCapability,
	*capability.ResourcesCapability,
	*capability.PromptsCapability,
	*capability.CompletionCapability,
	error,
) {
	sessionManager, err := mcp.NewManager(logger, cfg)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create session manager: %w", err)
	}
	sessionManager.AddValidator(validators.CreateDefaultValidators()...)

	// --- Initialize Capabilities ---
	baseCapability := capability.NewBase(logger, sessionManager)
	toolsCapability := capability.NewToolsCapability(sessionManager, logger)
	resourcesCapability := capability.NewResourcesCapability(sessionManager, logger)
	promptsCapability := capability.NewPromptsCapability(logger, sessionManager)
	completionCapability := capability.NewCompletionCapability(logger)
	// Add more capabilities here if needed

	// Register capabilities with the session manager's input processor
	sessionManager.AddCapability(
		baseCapability,
		toolsCapability,
		resourcesCapability,
		promptsCapability,
		completionCapability,
	)

	// --- Set up transport ---
	sseTransport, err := transport.New(sessionManager, logger, cfg)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create server transport: %w", err)
	}

	// --- Set up HTTP server ---
	mux := http.NewServeMux()
	sseTransport.RegisterHandlers(mux)

	// Register status handler
	logger.Info("Registering status handler", zap.String("path", "/status"))
	mux.HandleFunc("/status", extra.StatusHandler(cfg, logger))

	// --- Start HTTP Server using Shared Utility ---
	serverInstance, listenerErrChan, startErr := transport.StartHTTPServer(
		ctx,
		logger,
		cfg,
		mux,
		overwriteListenAddr,
	)
	if startErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to start HTTP server: %w", startErr)
	}

	// --- Goroutine to handle listener errors and graceful shutdown ---
	go func() {
		select {
		case err, ok := <-listenerErrChan:
			if ok && err != nil && !errors.Is(err, http.ErrServerClosed) {
				// Log fatal error if the listener fails unexpectedly after start
				logger.Fatal("Server listener failed", zap.Error(err))
				os.Exit(1) // Ensure exit if listener fails catastrophically
			}
			logger.Info("Server listener stopped.")
		case <-ctx.Done():
			logger.Info("Shutdown signal received, stopping server...")
			// Create shutdown context with timeout
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Timeout for shutdown
			defer cancel()

			// Close MCP sessions
			sessionManager.CloseAllSessions()

			// Shutdown HTTP server using the shared utility function
			transport.ShutdownHTTPServer(shutdownCtx, logger, serverInstance)
			logger.Info("Server stopped.")
		}
	}()

	// Return the capabilities so the caller (e.g., startExample) can configure them
	return toolsCapability, resourcesCapability, promptsCapability, completionCapability, nil
}
