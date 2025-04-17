package env

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/gate4ai/mcp/gateway" // Actual import path for gateway
	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
)

const GatewayComponentName = "gateway"

// GatewayServerEnv manages the MCP Gateway server process.
type GatewayServerEnv struct {
	BaseEnv
	port       int
	url        string // Intended/Actual URL (public facing)
	stopFunc   context.CancelFunc
	dbConfig   *config.DatabaseConfig // Hold config to close it
	logger     *zap.Logger
	mux        sync.RWMutex
	shutdownWg sync.WaitGroup // WaitGroup for graceful shutdown
}

// NewGatewayServerEnv creates a new gateway server component.
func NewGatewayServerEnv() *GatewayServerEnv {
	// Initialize logger here or get from a shared test setup
	logger, _ := zap.NewDevelopment() // Use Development logger for tests

	return &GatewayServerEnv{
		BaseEnv: BaseEnv{name: GatewayComponentName},
		logger:  logger.With(zap.String("component", GatewayComponentName)),
	}
}

// Configure allocates a port for the gateway and declares dependencies.
func (e *GatewayServerEnv) Configure(envs *Envs) (dependencies []string, err error) {
	port, err := envs.GetFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to get free port for gateway: %w", err)
	}

	e.mux.Lock()
	e.port = port
	e.url = fmt.Sprintf("http://localhost:%d", port) // Public URL
	e.mux.Unlock()

	log.Printf("%s: Intends to run on public URL: %s", e.Name(), e.url)
	os.Setenv("GATE4AI_GATEWAY_URL", e.url) // Set env var for other components if needed early

	// Depends on DB settings being applied and the portal being configured (to get its internal URL later).
	// Database must also be configured.
	return []string{DBSettingsComponentName}, nil
}

// Start launches the Go gateway server.
func (e *GatewayServerEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		e.mux.RLock()
		port := e.port
		intendedURL := e.url
		e.mux.RUnlock()

		if port == 0 {
			resultChan <- fmt.Errorf("%s: port not allocated in Configure phase", e.Name())
			return
		}

		dbURL := envs.GetURL(DBComponentName)
		if dbURL == "" {
			resultChan <- fmt.Errorf("%s: database URL not available", e.Name())
			return
		}

		// Portal internal URL is needed by gateway config/runtime
		// portalInternalURL := envs.GetURL(PortalComponentName)
		// if portalInternalURL == "" {
		// 	resultChan <- fmt.Errorf("%s: portal internal URL not available", e.Name())
		// 	return
		// }
		// NOTE: Gateway fetches portal URL from DB settings now, so direct dependency might not be needed here,
		// but the dependency in Configure ensures DB settings are applied first.

		// Server context for managing the gateway lifecycle
		// Use background context because termination is handled by Stop() calling cancelFunc
		serverCtx, cancel := context.WithCancel(context.Background())

		// --- Setup Gateway ---
		var err error
		e.dbConfig, err = config.NewDatabaseConfig(dbURL, e.logger) // Use component's logger
		if err != nil {
			cancel()
			resultChan <- fmt.Errorf("%s: failed to create gateway database config: %w", e.Name(), err)
			return
		}

		listenAddr := fmt.Sprintf(":%d", port)

		// --- Start Gateway in Goroutine ---
		e.shutdownWg.Add(1) // Increment WaitGroup before starting
		go func() {
			defer e.shutdownWg.Done() // Decrement WaitGroup when goroutine exits

			e.logger.Info("Starting gateway node", zap.String("listenAddr", listenAddr))
			// gateway.Start might block until shutdown or return an error immediately
			node, startErr := gateway.Start(serverCtx, e.logger, e.dbConfig, listenAddr)

			if startErr != nil {
				e.logger.Error("Failed to start gateway node", zap.Error(startErr))
				// If Start fails immediately, signal the outer goroutine
				// We need a way to signal this failure back to the resultChan
				// Using a separate channel for async start errors
				select {
				case resultChan <- fmt.Errorf("%s: gateway.Start failed: %w", e.Name(), startErr):
				default: // Avoid blocking if resultChan already used
					log.Printf("%s: Failed to send immediate start error to channel", e.Name())
				}
				cancel() // Ensure context is cancelled if start fails
				return
			}
			e.logger.Info("Gateway node started.")

			// Wait for context cancellation to trigger shutdown
			<-serverCtx.Done()
			e.logger.Info("Shutdown signal received, stopping gateway node...")
			if !node.WaitForShutdown(5 * time.Second) {
				e.logger.Error("Gateway graceful shutdown failed")
			} else {
				e.logger.Info("Gateway node shut down gracefully.")
			}
			// Close config AFTER shutdown
			if e.dbConfig != nil {
				e.dbConfig.Close()
				e.logger.Info("Closed gateway database config.")
			}
		}()

		// Store the cancel function for Stop()
		e.mux.Lock()
		e.stopFunc = cancel
		e.mux.Unlock()

		// --- Wait for Readiness ---
		readinessURL := fmt.Sprintf("%s/status", intendedURL) // Assuming gateway has /status endpoint
		// Use parent context for readiness check timeout
		waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second) // Readiness timeout
		defer waitCancel()

		// Need to handle the case where gateway.Start failed immediately
		readinessCheckDone := make(chan error, 1)
		go func() {
			readinessCheckDone <- waitForServer(waitCtx, readinessURL, 60*time.Second)
		}()

		select {
		case err = <-readinessCheckDone:
			if err != nil {
				log.Printf("%s: Readiness check failed: %v", e.Name(), err)
				e.Stop() // Attempt to stop the misbehaving process
				// Check if the error wasn't already sent by the start goroutine
				select {
				case <-resultChan: // Error already sent or channel closed
				default:
					resultChan <- fmt.Errorf("%s: server readiness check failed: %w", e.Name(), err)
				}
				return
			}
		case <-ctx.Done(): // Parent context cancelled
			log.Printf("%s: Readiness check cancelled by parent context.", e.Name())
			e.Stop()
			resultChan <- ctx.Err()
			return
			// No need to check resultChan here, parent context cancellation takes priority
		}

		log.Printf("%s: Server is ready on %s.", e.Name(), intendedURL)
		resultChan <- nil // Signal success (only if readiness check passed)
	}()

	return resultChan
}

// Stop triggers the graceful shutdown of the gateway server.
func (e *GatewayServerEnv) Stop() error {
	e.mux.Lock()
	cancel := e.stopFunc
	e.stopFunc = nil // Prevent double stopping
	e.mux.Unlock()

	if cancel != nil {
		log.Printf("%s: Triggering shutdown...", e.Name())
		cancel() // Signal the gateway's context to cancel

		// Wait for the shutdown goroutine to complete
		log.Printf("%s: Waiting for shutdown completion...", e.Name())
		e.shutdownWg.Wait() // Wait for the goroutine started in Start to finish
		log.Printf("%s: Shutdown complete.", e.Name())

	} else {
		log.Printf("%s: Server already stopped or not started.", e.Name())
	}

	// Close config just in case shutdown logic didn't run (e.g., start failed before goroutine setup)
	e.mux.RLock()
	cfg := e.dbConfig
	e.mux.RUnlock()
	if cfg != nil {
		cfg.Close()
	}

	return nil
}

// URL returns the public URL the gateway server listens on.
func (e *GatewayServerEnv) URL() string {
	e.mux.RLock()
	defer e.mux.RUnlock()
	return e.url
}
