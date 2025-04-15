package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	gwCapabilities "github.com/gate4ai/mcp/gateway/capability"
	"github.com/gate4ai/mcp/gateway/discovering"
	"github.com/gate4ai/mcp/gateway/extra"
	serverextra "github.com/gate4ai/mcp/server/extra"
	"github.com/gate4ai/mcp/server/mcp"
	serverCapabilities "github.com/gate4ai/mcp/server/mcp/capability"
	"github.com/gate4ai/mcp/server/mcp/validators"
	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
)

// Node represents the main gateway component that coordinates all services
type Node struct {
	logger          *zap.Logger
	cfg             config.IConfig
	serverTransport *transport.Transport
	sessionManager  *mcp.Manager
	httpServer      *http.Server   // Store the server instance
	listenerErrChan <-chan error   // Channel for listener errors
	shutdownWg      sync.WaitGroup // WaitGroup for shutdown
}

// NodeOption is a functional option for configuring the Node
type NodeOption func(*Node) error

// New creates a new gateway node with the provided logger and config
func New(logger *zap.Logger, cfg config.IConfig) (*Node, error) {
	if logger == nil {
		// Default logger if needed, though Start usually provides one
		logger, _ = zap.NewProduction()
	}
	if cfg == nil {
		return nil, errors.New("config cannot be nil")
	}
	n := &Node{
		logger: logger.Named("gateway-node"), // Add name for clarity
		cfg:    cfg,
		// shutdownWg initialization needed
	}
	n.shutdownWg.Add(1) // Initialize WaitGroup counter for the main server loop

	var err error
	n.sessionManager, err = mcp.NewManager(n.logger, n.cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create session manager: %w", err)
	}
	// Add default validators and gateway-specific capabilities
	n.sessionManager.AddValidator(validators.CreateDefaultValidators()...)
	n.sessionManager.AddCapability(
		serverCapabilities.NewBase(n.logger, n.sessionManager), // Base MCP handlers
		gwCapabilities.NewGatewayCapability(n.logger, n.cfg),   // Gateway routing logic
	)
	n.serverTransport, err = transport.New(n.sessionManager, n.logger, n.cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create server transport: %w", err)
	}
	return n, nil
}

// Start initializes and starts all components of the node
func (n *Node) Start(ctx context.Context, mux *http.ServeMux, overwriteListenAddr string) error {
	n.logger.Info("Starting gateway node...")

	// --- Register Handlers ---
	n.serverTransport.RegisterMCPHandlers(mux)

	discoveringHandlerPath, err := n.cfg.DiscoveringHandlerPath()
	if err != nil {
		n.logger.Warn("Failed to get info handler path from config", zap.Error(err))
	} else if discoveringHandlerPath != "" {
		n.logger.Info("Registering info handler", zap.String("path", discoveringHandlerPath))
		mux.HandleFunc(discoveringHandlerPath, discovering.Handler(n.logger))
	}

	n.logger.Info("Registering status handler", zap.String("path", "/status"))
	mux.HandleFunc("/status", serverextra.StatusHandler(n.cfg, n.logger))

	frontendAddress, err := n.cfg.FrontendAddressForProxy()
	if err != nil {
		n.logger.Warn("Failed to get frontend address for proxy from config", zap.Error(err))
	} else if frontendAddress != "" {
		n.logger.Info("Registering proxy handler", zap.String("frontend_address", frontendAddress))
		proxyHandler := extra.ProxyHandler(frontendAddress, n.logger)
		if proxyHandler != nil {
			mux.HandleFunc("/", proxyHandler) // Proxy root and unmatched paths
		} else {
			n.logger.Error("Failed to create proxy handler")
		}
	}

	// --- Start HTTP Server using Shared Utility ---
	serverInstance, listenerErrChan, startErr := transport.StartHTTPServer(
		ctx,
		n.logger,
		n.cfg,
		mux,
		overwriteListenAddr,
	)
	if startErr != nil {
		n.shutdownWg.Done() // Decrement counter if startup fails
		return fmt.Errorf("failed to start HTTP server: %w", startErr)
	}
	n.httpServer = serverInstance
	n.listenerErrChan = listenerErrChan

	// --- Goroutine to handle listener errors ---
	go func() {
		defer n.shutdownWg.Done() // Signal completion when this goroutine exits
		select {
		case err, ok := <-n.listenerErrChan:
			if ok && err != nil {
				// This error occurred *after* successful startup
				n.logger.Error("Gateway HTTP/S listener failed", zap.Error(err))
				// Depending on the application, you might want to trigger a shutdown here
				// or attempt a restart. For now, just log it.
			}
		case <-ctx.Done():
			// Context cancelled, shutdown initiated elsewhere
			n.logger.Info("Listener error monitor stopped due to context cancellation.")
		}
	}()

	// --- Graceful Shutdown Logic ---
	go func() {
		<-ctx.Done() // Wait for cancellation signal (e.g., from main)
		n.logger.Info("Shutdown signal received, stopping Gateway node...")

		// Create shutdown context with timeout
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // Generous timeout
		defer cancel()

		// Close MCP sessions first
		n.sessionManager.CloseAllSessions()

		// Shutdown HTTP server using the shared utility function
		transport.ShutdownHTTPServer(shutdownCtx, n.logger, n.httpServer)

		// The server goroutine started by StartHTTPServer will detect ErrServerClosed
		// and the listenerErrChan goroutine will then call shutdownWg.Done().
	}()

	listenAddr, _ := n.cfg.ListenAddr() // Get address again for logging
	sslEnabled, _ := n.cfg.SSLEnabled()
	n.logger.Info("Gateway node started successfully", zap.String("addr", listenAddr), zap.Bool("sslEnabled", sslEnabled))
	return nil
}

// WaitForShutdown waits for the node's main server loop to finish.
func (n *Node) WaitForShutdown(timeout time.Duration) bool {
	doneChan := make(chan struct{})
	go func() {
		n.shutdownWg.Wait() // Wait for the main server loop goroutine to finish
		close(doneChan)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-doneChan:
		n.logger.Info("Gateway node shutdown complete.")
		return true // Clean shutdown completed
	case <-timer.C:
		n.logger.Warn("Gateway node shutdown timed out.")
		return false // Timeout occurred
	}
}

// Start is a convenience function to create and start the node
func Start(ctx context.Context, logger *zap.Logger, cfg config.IConfig, overwriteListenAddr string) (*Node, error) {
	node, err := New(logger, cfg)
	if err != nil {
		// Use Fatalf only if called directly from main, otherwise return error
		return nil, fmt.Errorf("failed to create gateway node: %w", err)
	}

	// Use default ServeMux
	mux := http.NewServeMux()

	if err := node.Start(ctx, mux, overwriteListenAddr); err != nil {
		// Use Fatalf only if called directly from main
		return nil, fmt.Errorf("gateway node failed to start: %w", err)
	}
	return node, nil
}
