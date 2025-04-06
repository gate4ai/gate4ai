package server

import (
	"context"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/gate4ai/mcp/server/extra"
	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/server/mcp/capability"
	"github.com/gate4ai/mcp/server/mcp/validators"
	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared/config"
)

// StartServer starts the MCP SSE server with the provided options
func StartServer(ctx context.Context, logger *zap.Logger, cfg config.IConfig, overwriteListenAddr string) (
	*capability.ToolsCapability,
	*capability.ResourcesCapability,
	*capability.PromptsCapability,
	*capability.CompletionCapability,
	error,
) {
	sessionManager, err := mcp.NewManager(logger, cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sessionManager.AddValidator(validators.CreateDefaultValidators()...)

	baseCapability := capability.NewBase(logger, sessionManager)
	toolsCapability := capability.NewToolsCapability(sessionManager, logger)
	resourcesCapability := capability.NewResourcesCapability(sessionManager, logger)
	promptsCapability := capability.NewPromptsCapability(logger, sessionManager)
	completionCapability := capability.NewCompletionCapability(logger)

	sessionManager.AddCapability(baseCapability, toolsCapability, resourcesCapability, promptsCapability, completionCapability)

	// Set up transport and HTTP server
	sseTransport, err := transport.New(sessionManager, logger, cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	mux := http.NewServeMux()
	sseTransport.RegisterHandlers(mux)

	// Register status handler
	logger.Info("Registering status handler", zap.String("path", "/status"))
	mux.HandleFunc("/status", extra.StatusHandler(cfg, logger))

	var listenAddr string
	if overwriteListenAddr == "" {
		listenAddr, err = cfg.ListenAddr()
		if err != nil {
			logger.Error("Failed to get listen address", zap.Error(err))
			return nil, nil, nil, nil, err
		}
	} else {
		listenAddr = overwriteListenAddr
	}
	server := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	// Start HTTP server in a goroutine
	logger.Debug("Starting HTTP server", zap.String("addr", listenAddr))
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", zap.Error(err))
			os.Exit(1)
		}
	}()

	// Set up graceful shutdown
	go func() {
		<-ctx.Done()
		logger.Info("Shutting down HTTP server")

		// Create a deadline for server shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown error", zap.Error(err))
		}

		logger.Info("HTTP server stopped")
	}()

	return toolsCapability, resourcesCapability, promptsCapability, completionCapability, nil
}
