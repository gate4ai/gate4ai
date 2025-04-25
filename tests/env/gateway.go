package env

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/gateway" // Actual import path for gateway
	"github.com/gate4ai/gate4ai/shared/config"
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
	node       *gateway.Node  // Store the started node for graceful shutdown
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

	log.Printf("[%s] Configuring component. Intends to run on public URL: %s", e.Name(), e.url)
	os.Setenv("GATE4AI_GATEWAY_URL", e.url) // Set env var for other components if needed early

	// Depends on DB settings being applied and the portal being configured (to get its internal URL later).
	// Database must also be configured.
	log.Printf("[%s] Declaring dependencies: %v", e.Name(), []string{DBSettingsComponentName, PortalComponentName}) // Portal needed for proxy URL in DB settings
	return []string{DBSettingsComponentName, PortalComponentName}, nil
}

// Start launches the Go gateway server.
func (e *GatewayServerEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		logPrefix := fmt.Sprintf("[%s] ", e.Name())
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

		e.mux.RLock()
		port := e.port
		intendedURL := e.url
		e.mux.RUnlock()

		if port == 0 {
			err := fmt.Errorf("%sport not allocated in Configure phase", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sUsing port: %d", logPrefix, port)

		log.Printf("%sFetching database URL...", logPrefix)
		dbURL := envs.GetURL(DBComponentName)
		if dbURL == "" {
			err := fmt.Errorf("%sdatabase URL not available", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sDatabase URL obtained.", logPrefix)

		// Server context for managing the gateway lifecycle
		serverCtx, cancel := context.WithCancel(context.Background()) // Use background, manage via Stop()

		// --- Setup Gateway ---
		log.Printf("%sCreating database config...", logPrefix)
		var err error
		e.dbConfig, err = config.NewDatabaseConfig(dbURL, e.logger) // Use component's logger
		if err != nil {
			cancel()
			err = fmt.Errorf("%sfailed to create gateway database config: %w", logPrefix, err)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sDatabase config created.", logPrefix)

		listenAddr := fmt.Sprintf(":%d", port)

		// --- Start Gateway in Goroutine ---
		e.shutdownWg.Add(1) // Increment WaitGroup before starting
		go func() {
			defer e.shutdownWg.Done() // Decrement WaitGroup when goroutine exits

			e.logger.Info("Starting gateway node", zap.String("listenAddr", listenAddr))
			startStartTime := time.Now()
			node, startErr := gateway.Start(serverCtx, e.logger, e.dbConfig, listenAddr)
			startDuration := time.Since(startStartTime)

			if startErr != nil {
				e.logger.Error("Failed to start gateway node", zap.Error(startErr), zap.Duration("duration", startDuration))
				select {
				case resultChan <- fmt.Errorf("%s: gateway.Start failed: %w", e.Name(), startErr):
				default:
					log.Printf("%s: Failed to send immediate start error to channel", e.Name())
				}
				cancel() // Ensure context is cancelled if start fails
				return
			}
			e.logger.Info("Gateway node started.", zap.Duration("duration", startDuration))
			e.mux.Lock()
			e.node = node // Store the node instance
			e.mux.Unlock()

			// Wait for context cancellation to trigger shutdown
			<-serverCtx.Done()
			e.logger.Info("Shutdown signal received, stopping gateway node...")
			if !node.WaitForShutdown(5 * time.Second) { // Use node's method
				e.logger.Error("Gateway graceful shutdown failed")
			} else {
				e.logger.Info("Gateway node shut down gracefully.")
			}
			// Config closing handled in Stop() or here if preferred after WaitForShutdown
		}()

		// Store the cancel function for Stop()
		e.mux.Lock()
		e.stopFunc = cancel
		e.mux.Unlock()

		// --- Wait for Readiness ---
		readinessURL := fmt.Sprintf("%s/status", intendedURL) // Assuming gateway has /status endpoint
		log.Printf("%sChecking readiness at %s...", logPrefix, readinessURL)
		// Use parent context for readiness check timeout
		waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second) // Readiness timeout
		defer waitCancel()

		readinessCheckDone := make(chan error, 1)
		go func() {
			readinessCheckDone <- waitForServer(waitCtx, readinessURL, 60*time.Second)
		}()

		select {
		case err = <-readinessCheckDone:
			if err != nil {
				log.Printf("%sERROR: Readiness check failed: %v", logPrefix, err)
				e.Stop() // Attempt to stop the misbehaving process
				// Check if the error wasn't already sent by the start goroutine
				select {
				case <-resultChan: // Error already sent or channel closed
				default:
					resultChan <- fmt.Errorf("%sserver readiness check failed: %w", logPrefix, err)
				}
				return
			}
		case <-ctx.Done(): // Parent context cancelled
			log.Printf("%sReadiness check cancelled by parent context.", logPrefix)
			e.Stop()
			resultChan <- ctx.Err()
			return
		}

		log.Printf("%sServer is ready.", logPrefix)
		resultChan <- nil // Signal success (only if readiness check passed)
	}()

	return resultChan
}

// Stop triggers the graceful shutdown of the gateway server.
func (e *GatewayServerEnv) Stop() error {
	logPrefix := fmt.Sprintf("[%s] ", e.Name())
	e.mux.Lock()
	cancel := e.stopFunc
	e.stopFunc = nil // Prevent double stopping
	e.mux.Unlock()

	if cancel != nil {
		log.Printf("%sTriggering shutdown...", logPrefix)
		cancel() // Signal the gateway's context to cancel

		// Wait for the shutdown goroutine to complete
		log.Printf("%sWaiting for shutdown completion...", logPrefix)
		e.shutdownWg.Wait() // Wait for the goroutine started in Start to finish
		log.Printf("%sShutdown complete.", logPrefix)

	} else {
		log.Printf("%sServer already stopped or not started.", logPrefix)
	}

	// Close config AFTER shutdown is complete
	e.mux.RLock()
	cfg := e.dbConfig
	e.mux.RUnlock()
	if cfg != nil {
		log.Printf("%sClosing database config...", logPrefix)
		cfg.Close()
		log.Printf("%sDatabase config closed.", logPrefix)
	}

	return nil
}

// URL returns the public URL the gateway server listens on.
func (e *GatewayServerEnv) URL() string {
	e.mux.RLock()
	defer e.mux.RUnlock()
	return e.url
}
