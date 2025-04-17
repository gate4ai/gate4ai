package env

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	// Adjust these imports based on actual project structure
	"github.com/gate4ai/gate4ai/server"
	"github.com/gate4ai/gate4ai/server/cmd/mcp-example-server/exampleCapability"
	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared/config"
	"go.uber.org/zap"
)

const ExampleServerComponentName = "mcp-example"

// ExampleServerDetails holds the specific URLs for the example server endpoints.
type ExampleServerDetails struct {
	BaseURL    string
	MCP2024URL string
	MCP2025URL string
	TestAPIKey string
}

// ExampleServerEnv manages the Example MCP server process.
type ExampleServerEnv struct {
	BaseEnv
	port       int
	baseURL    string // Intended/Actual base URL (http://host:port)
	details    ExampleServerDetails
	stopFunc   context.CancelFunc
	yamlConfig *config.YamlConfig
	logger     *zap.Logger
	mux        sync.RWMutex
	shutdownWg sync.WaitGroup
	errChan    <-chan error // Store error channel from server.Start
}

// NewExampleServerEnv creates a new example server component.
func NewExampleServerEnv() *ExampleServerEnv {
	logger, _ := zap.NewDevelopment() // Use Development logger for tests
	return &ExampleServerEnv{
		BaseEnv: BaseEnv{name: ExampleServerComponentName},
		logger:  logger.With(zap.String("component", ExampleServerComponentName)),
	}
}

// Configure allocates a port and declares dependencies.
func (e *ExampleServerEnv) Configure(envs *Envs) (dependencies []string, err error) {
	port, err := envs.GetFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to get free port for example server: %w", err)
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)
	testAPIKey := "test-key-user1" // Hardcoded test key from original setup

	e.mux.Lock()
	e.port = port
	e.baseURL = baseURL
	// Pre-calculate details based on intended URL
	e.details = ExampleServerDetails{
		BaseURL:    baseURL,
		MCP2024URL: fmt.Sprintf("%s%s?key=%s", baseURL, transport.MCP2024_PATH, testAPIKey),
		MCP2025URL: fmt.Sprintf("%s%s?key=%s", baseURL, transport.MCP2025_PATH, testAPIKey),
		TestAPIKey: testAPIKey,
	}
	e.mux.Unlock()

	log.Printf("[%s] Configuring component. Intends to run on base URL: %s", e.Name(), baseURL)
	os.Setenv("GATE4AI_EXAMPLE_SERVER_URL", e.details.MCP2024URL) // Set one for compatibility

	log.Printf("[%s] Declaring dependencies: %v", e.Name(), []string{DBComponentName}) // Assume it might read config from DB eventually
	return []string{DBComponentName}, nil                                              // Depends on DB for potential config reads
}

// Start launches the Go example server.
func (e *ExampleServerEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		logPrefix := fmt.Sprintf("[%s] ", e.Name())
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

		e.mux.RLock()
		port := e.port
		intendedURL := e.baseURL
		e.mux.RUnlock()

		if port == 0 {
			err := fmt.Errorf("%sport not allocated in Configure phase", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sUsing port: %d", logPrefix, port)

		// Server context for managing the server lifecycle
		serverCtx, cancel := context.WithCancel(context.Background())

		// --- Setup Example Server ---
		log.Printf("%sLoading YAML config...", logPrefix)
		var err error
		configPath := filepath.Join(TestConfigWorkspaceFolder, "server/cmd/mcp-example-server/config.yaml")
		e.yamlConfig, err = config.NewYamlConfig(configPath, e.logger)
		if err != nil {
			cancel()
			err = fmt.Errorf("%sfailed to create example server yaml config: %w", logPrefix, err)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sYAML config loaded from %s.", logPrefix, configPath)

		listenAddr := fmt.Sprintf(":%d", port)
		serverOptions := exampleCapability.BuildOptions(e.logger)
		serverOptions = append(serverOptions, server.WithListenAddr(listenAddr))
		log.Printf("%sServer options prepared.", logPrefix)

		// Store the cancel function
		e.mux.Lock()
		e.stopFunc = cancel
		e.mux.Unlock()

		// --- Start Server in Goroutine ---
		e.shutdownWg.Add(1)
		go func() {
			defer e.shutdownWg.Done()
			e.logger.Info("Starting example server process", zap.String("listenAddr", listenAddr))
			startStartTime := time.Now()

			// server.Start blocks until shutdown or errors
			errChan, startErr := server.Start(serverCtx, e.logger, e.yamlConfig, serverOptions...)
			startDuration := time.Since(startStartTime)

			if startErr != nil {
				e.logger.Error("Failed to start example server immediately", zap.Error(startErr), zap.Duration("duration", startDuration))
				select {
				case resultChan <- fmt.Errorf("%s: server.Start failed immediately: %w", e.Name(), startErr):
				default:
					log.Printf("%s: Failed to send immediate start error to channel", e.Name())
				}
				cancel() // Ensure context is cancelled
				return
			}
			e.logger.Info("Example server process launched.", zap.Duration("duration", startDuration))
			e.mux.Lock()
			e.errChan = errChan // Store the error channel from server.Start
			e.mux.Unlock()

			// Wait for context cancellation signal or error from server.Start's listener
			select {
			case err := <-errChan:
				if err != nil {
					e.logger.Error("Example server listener failed", zap.Error(err))
					// Don't send to resultChan here, the main Start routine handles readiness check failure
				} else {
					e.logger.Info("Example server listener stopped gracefully.")
				}
			case <-serverCtx.Done():
				e.logger.Info("Shutdown signal received, stopping example server...")
			}

			// Config closing handled in Stop()
			e.logger.Info("Example server goroutine finished.")
		}()

		// --- Wait for Readiness ---
		readinessURL := fmt.Sprintf("%s/status", intendedURL) // Assuming /status endpoint
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
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop triggers the graceful shutdown of the example server.
func (e *ExampleServerEnv) Stop() error {
	logPrefix := fmt.Sprintf("[%s] ", e.Name())
	e.mux.Lock()
	cancel := e.stopFunc
	cfg := e.yamlConfig
	e.stopFunc = nil // Prevent double stopping
	e.mux.Unlock()

	if cancel != nil {
		log.Printf("%sTriggering shutdown...", logPrefix)
		cancel() // Signal the server's context to cancel

		// Wait for the shutdown goroutine to complete
		log.Printf("%sWaiting for shutdown completion...", logPrefix)
		e.shutdownWg.Wait() // Wait for the server.Start goroutine to finish
		log.Printf("%sShutdown complete.", logPrefix)
	} else {
		log.Printf("%sServer already stopped or not started.", logPrefix)
	}

	// Close config AFTER shutdown is complete
	if cfg != nil {
		log.Printf("%sClosing YAML config...", logPrefix)
		cfg.Close()
		log.Printf("%sYAML config closed.", logPrefix)
	}

	return nil
}

// URL returns the base URL (http://host:port) for the example server.
func (e *ExampleServerEnv) URL() string {
	e.mux.RLock()
	defer e.mux.RUnlock()
	return e.baseURL
}

// GetDetails returns the specific endpoint URLs.
func (e *ExampleServerEnv) GetDetails() interface{} {
	e.mux.RLock()
	defer e.mux.RUnlock()
	// Return a copy
	return e.details
}
