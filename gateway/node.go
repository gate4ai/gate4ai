package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	gwCapabilities "github.com/gate4ai/mcp/gateway/capability"
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
	done            chan struct{}
}

// NodeOption is a functional option for configuring the Node
type NodeOption func(*Node) error

// New creates a new gateway node with the provided logger and config
func New(logger *zap.Logger, cfg config.IConfig) (n *Node, err error) {
	if logger == nil && cfg == nil {
		return nil, fmt.Errorf("logger is required")
	}
	n = &Node{
		logger: logger,
		cfg:    cfg,
		done:   make(chan struct{}),
	}
	n.sessionManager, err = mcp.NewManager(n.logger, n.cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create session manager: %w", err)
	}
	n.sessionManager.AddValidator(validators.CreateDefaultValidators()...)
	n.sessionManager.AddCapability(
		serverCapabilities.NewBase(logger, n.sessionManager),
		gwCapabilities.NewGatewayCapability(n.logger, n.cfg))
	n.serverTransport, err = transport.New(n.sessionManager, n.logger, n.cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create server transport: %w", err)
	}
	return n, nil
}

// Start initializes and starts all components of the node
func (n *Node) Start(ctx context.Context, mux *http.ServeMux, overwriteListenAddr string) (err error) {
	n.logger.Info("Starting gateway node")
	n.serverTransport.RegisterHandlers(mux)

	// Register info handler if configured
	infoHandler, err := n.cfg.InfoHandler()
	if err != nil {
		n.logger.Error("Failed to get info handler path", zap.Error(err))
	} else if infoHandler != "" {
		n.logger.Info("Registering info handler", zap.String("path", infoHandler))
		mux.HandleFunc(infoHandler, extra.InfoHandler(n.logger))
	}

	// Register status handler
	n.logger.Info("Registering status handler", zap.String("path", "/status"))
	mux.HandleFunc("/status", serverextra.StatusHandler(n.cfg, n.logger))

	// Get frontend URL for proxy if configured
	frontendAddressForProxy, err := n.cfg.FrontendAddressForProxy()
	if err != nil {
		n.logger.Error("Failed to get frontend address for proxy", zap.Error(err))
	}

	// Register proxy handler if configured
	if frontendAddressForProxy != "" {
		n.logger.Info("Registering proxy handler", zap.String("frontend_address", frontendAddressForProxy))
		mux.HandleFunc("/", extra.ProxyHandler(frontendAddressForProxy, n.logger))
	}

	listenAddr := ""
	if overwriteListenAddr == "" {
		listenAddr, err = n.cfg.ListenAddr()
		if err != nil {
			n.logger.Error("Failed to get listen address", zap.Error(err))
			return fmt.Errorf("failed to get listen address: %w", err)
		}
	} else {
		listenAddr = overwriteListenAddr
	}

	httpServer := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			n.logger.Error("HTTP server error", zap.Error(err))
		}
	}()

	// Monitor the parent context for cancellation
	go func() {
		<-ctx.Done()

		n.sessionManager.CloseAllSessions()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			n.logger.Error("HTTP server shutdown error:", zap.Error(err))
		}

		n.logger.Info("Gateway node stopped")
		close(n.done)
	}()

	n.logger.Info("Gateway node started successfully", zap.String("addr", listenAddr))
	return nil
}

func (n *Node) WaitForShutdown(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-n.done:
		return true // Clean shutdown completed
	case <-timer.C:
		n.logger.Warn("Shutdown timeout reached, forcing exit")
		return false // Timeout occurred
	}
}

func Start(ctx context.Context, logger *zap.Logger, cfg config.IConfig, overwriteListenAddr string) (node *Node, err error) {
	// Create and start the node
	node, err = New(logger, cfg)
	if err != nil {
		logger.Fatal("Failed to create node", zap.Error(err))
	}
	// Start the node
	if err := node.Start(ctx, http.NewServeMux(), overwriteListenAddr); err != nil {
		logger.Fatal("Node failed to start", zap.Error(err))
	}
	return node, err
}
