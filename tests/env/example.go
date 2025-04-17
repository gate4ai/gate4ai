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
	"github.com/gate4ai/mcp/server"
	"github.com/gate4ai/mcp/server/cmd/mcp-example-server/exampleCapability"
	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared/config"
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

	log.Printf("%s: Intends to run on base URL: %s", e.Name(), baseURL)
	os.Setenv("GATE4AI_EXAMPLE_SERVER_URL", e.details.MCP2024URL) // Set one for compatibility

	// Example server might depend on DB setup if it uses config, adjust as needed.
	// Assuming it primarily uses its yaml config.
	return []string{DBComponentName}, nil // Depends on DB for potential config reads
}

// Start launches the Go example server.
func (e *ExampleServerEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		e.mux.RLock()
		port := e.port
		intendedURL := e.baseURL
		e.mux.RUnlock()

		if port == 0 {
			resultChan <- fmt.Errorf("%s: port not allocated in Configure phase", e.Name())
			return
		}

		// Server context for managing the server lifecycle
		serverCtx, cancel := context.WithCancel(context.Background())

		// --- Setup Example Server ---
		var err error
		configPath := filepath.Join(TestConfigWorkspaceFolder, "server/cmd/mcp-example-server/config.yaml")
		e.yamlConfig, err = config.NewYamlConfig(configPath, e.logger)
		if err != nil {
			cancel()
			resultChan <- fmt.Errorf("%s: failed to create example server yaml config: %w", e.Name(), err)
			return
		}

		listenAddr := fmt.Sprintf(":%d", port)
		serverOptions := exampleCapability.BuildOptions(e.logger)
		serverOptions = append(serverOptions, server.WithListenAddr(listenAddr))

		// --- Start Server in Goroutine ---
		e.shutdownWg.Add(1)
		go func() {
			defer e.shutdownWg.Done()
			e.logger.Info("Starting example server", zap.String("listenAddr", listenAddr))

			// server.Start blocks until shutdown or errors
			_, startErr := server.Start(serverCtx, e.logger, e.yamlConfig, serverOptions...)

			if startErr != nil {
				// Don't log Fatal here, report error back
				e.logger.Error("Failed to start example server", zap.Error(startErr))
				select {
				case resultChan <- fmt.Errorf("%s: server.Start failed: %w", e.Name(), startErr):
				default:
					log.Printf("%s: Failed to send immediate start error to channel", e.Name())
				}
				cancel() // Ensure context is cancelled
				return
			}

			// Wait for context cancellation signal
			<-serverCtx.Done()
			e.logger.Info("Shutdown signal received, stopping example server...")
			// server.Start handles its own cleanup on context cancellation.
			// We still need to close the config.
			if e.yamlConfig != nil {
				e.yamlConfig.Close()
				e.logger.Info("Closed example server config.")
			}
			e.logger.Info("Example server shut down.")
		}()

		// Store the cancel function
		e.mux.Lock()
		e.stopFunc = cancel
		e.mux.Unlock()

		// --- Wait for Readiness ---
		readinessURL := fmt.Sprintf("%s/status", intendedURL) // Assuming /status endpoint
		waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second)
		defer waitCancel()

		readinessCheckDone := make(chan error, 1)
		go func() {
			readinessCheckDone <- waitForServer(waitCtx, readinessURL, 60*time.Second)
		}()

		select {
		case err = <-readinessCheckDone:
			if err != nil {
				log.Printf("%s: Readiness check failed: %v", e.Name(), err)
				e.Stop() // Attempt to stop
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
		}

		log.Printf("%s: Server is ready on %s.", e.Name(), intendedURL)
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop triggers the graceful shutdown of the example server.
func (e *ExampleServerEnv) Stop() error {
	e.mux.Lock()
	cancel := e.stopFunc
	e.stopFunc = nil // Prevent double stopping
	cfg := e.yamlConfig
	e.mux.Unlock()

	if cancel != nil {
		log.Printf("%s: Triggering shutdown...", e.Name())
		cancel() // Signal the server's context to cancel

		// Wait for the shutdown goroutine to complete
		log.Printf("%s: Waiting for shutdown completion...", e.Name())
		e.shutdownWg.Wait() // Wait for the server.Start goroutine to finish
		log.Printf("%s: Shutdown complete.", e.Name())
	} else {
		log.Printf("%s: Server already stopped or not started.", e.Name())
	}

	// Close config just in case (might be redundant if shutdown succeeded)
	if cfg != nil {
		cfg.Close()
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
