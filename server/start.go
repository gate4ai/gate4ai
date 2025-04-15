// server/startEmpty.go
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os" // Import reflect
	"strings"
	"time"

	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared"
	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"

	"github.com/gate4ai/mcp/server/extra"
	"github.com/gate4ai/mcp/server/mcp/validators"
)

// Start starts the MCP SSE server with the provided options
// It now only returns an error channel and an error.
func Start(ctx context.Context, logger *zap.Logger, cfg config.IConfig, options ...ServerOption) (
	<-chan error, // Listener error channel
	error,
) {
	if logger == nil {
		return nil, errors.New("logger cannot be nil")
	}
	if cfg == nil {
		return nil, errors.New("config cannot be nil")
	}

	// --- 1. Initialize ServerBuilder ---
	listenAddr, err := cfg.ListenAddr()
	if err != nil {
		return nil, fmt.Errorf("failed to get listen address: %w", err)
	}

	sessionManager, err := mcp.NewManager(logger, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create session manager: %w", err)
	}

	transportInstance, err := transport.New(sessionManager, logger, cfg) // Pass cfg for potential transport options
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	builder := &ServerBuilder{
		ctx:          ctx,
		logger:       logger,
		cfg:          cfg,
		listenAddr:   listenAddr, // Initial address from config
		manager:      sessionManager,
		transport:    transportInstance,
		mux:          http.NewServeMux(),
		capabilities: make([]shared.ICapability, 0),
	}

	// --- 2. Apply Server Options ---
	logger.Info("Applying server configuration options...")
	for _, option := range options {
		if err := option(builder); err != nil {
			return nil, fmt.Errorf("failed to apply server option: %w", err)
		}
	}
	logger.Info("Server options applied successfully.")

	// --- 3. Finalize Setup based on Builder State ---
	// Add default validators
	sessionManager.AddValidator(validators.CreateDefaultValidators()...)

	// Register capabilities stored in the map with the session manager's input processor
	if len(builder.capabilities) > 0 {
		logger.Info("Registering capabilities with session manager", zap.Int("count", len(builder.capabilities)))
		capsToRegister := make([]shared.ICapability, 0, len(builder.capabilities))
		capsToRegister = append(capsToRegister, builder.capabilities...)
		builder.manager.AddCapability(capsToRegister...) // Manager handles routing
	} else {
		logger.Info("No capabilities registered.")
	}

	// Conditionally register HTTP handlers based on flags set by options
	if builder.registerMCPRoutes {
		builder.transport.RegisterMCPHandlers(builder.mux)
	}
	if builder.registerA2ARoutes {
		// Fetch agent card base info from config
		// Construct agent URL based on listen address + A2A path
		// Need to determine scheme (http/https) based on config
		sslEnabled, _ := cfg.SSLEnabled()
		scheme := "http"
		if sslEnabled {
			scheme = "https"
		}
		// Assuming listenAddr might contain host (like localhost:8080) or just port (:8080)
		// TODO: Need a more robust way to get the *publicly accessible* URL
		hostPort := builder.listenAddr
		if strings.HasPrefix(hostPort, ":") {
			// Assume localhost if only port is given
			hostPort = "localhost" + hostPort
		}
		agentURL := fmt.Sprintf("%s://%s%s", scheme, hostPort, transport.A2A_PATH)

		a2aBaseInfo, err := cfg.GetA2ACardBaseInfo(agentURL) // Use constructed URL
		if err != nil {
			return nil, fmt.Errorf("failed to load A2A agent card base info from config: %w", err)
		}
		// Construct full agent card
		agentCard := a2aSchema.AgentCard{
			Name:             a2aBaseInfo.Name,
			Description:      a2aBaseInfo.Description,
			URL:              a2aBaseInfo.AgentURL,
			Provider:         a2aBaseInfo.Provider,
			Version:          a2aBaseInfo.Version,
			DocumentationURL: a2aBaseInfo.DocumentationURL,
			//Capabilities:       a2aCap.GetCapabilitiesStruct(), // Assume GetCapabilitiesStruct exists
			Authentication:     a2aBaseInfo.Authentication,
			DefaultInputModes:  a2aBaseInfo.DefaultInputModes,
			DefaultOutputModes: a2aBaseInfo.DefaultOutputModes,
			Skills:             builder.a2aSkills, // Use skills collected by options
		}
		builder.transport.RegisterA2AHandlers(builder.mux, agentCard)
	}

	// Register status handler
	logger.Info("Registering status handler", zap.String("path", "/status"))
	builder.mux.HandleFunc("/status", extra.StatusHandler(cfg, logger))

	// --- Start HTTP Server using Shared Utility ---
	serverInstance, listenerErrChan, startErr := transport.StartHTTPServer(
		ctx,
		logger,
		cfg,
		builder.mux,
		builder.listenAddr, // Use the potentially overridden address
	)
	if startErr != nil {
		return nil, fmt.Errorf("failed to start HTTP server: %w", startErr)
	}

	// --- Goroutine to handle listener errors and graceful shutdown ---
	go func() {
		select {
		case err, ok := <-listenerErrChan:
			if ok && err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Fatal("Server listener failed", zap.Error(err))
				os.Exit(1)
			}
			logger.Info("Server listener stopped.")
		case <-ctx.Done():
			logger.Info("Shutdown signal received, stopping server...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			sessionManager.CloseAllSessions()
			transport.ShutdownHTTPServer(shutdownCtx, logger, serverInstance)
			logger.Info("Server stopped.")
		}
	}()

	return listenerErrChan, nil
}

// --- Server Options ---

// WithListenAddr overrides the listen address from the config.
func WithListenAddr(addr string) ServerOption {
	return func(b *ServerBuilder) error {
		// Allow empty addr to mean "use config default"
		if addr != "" {
			b.listenAddr = addr
			b.logger.Info("Overriding listen address", zap.String("newAddress", addr))
		}
		return nil
	}
}

// WithSessionTimeout configures the idle session timeout (example).
func WithSessionTimeout(timeout time.Duration) ServerOption {
	return func(b *ServerBuilder) error {
		if b.transport == nil {
			return errors.New("transport not initialized in builder, cannot set session timeout")
		}
		// Apply timeout setting via transport option if available, or log
		b.logger.Info("Configuring session timeout (via transport option)", zap.Duration("timeout", timeout))
		return transport.WithSessionTimeout(timeout)(b.transport) // Apply option to transport
	}
}
