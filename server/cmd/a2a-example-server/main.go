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
	"github.com/gate4ai/gate4ai/server/a2a"                          // Import the server's a2a package
	"github.com/gate4ai/gate4ai/server/cmd/a2a-example-server/agent" // Import the new agent package
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/gate4ai/shared/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	// --- Basic Setup ---
	loggerConfig := zap.NewProductionConfig()
	loggerConfig.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	loggerConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel) // Default level
	logger, _ := loggerConfig.Build()
	defer logger.Sync()

	listenAddr := flag.String("listen", ":4000", "Address and port to listen on (e.g., :4000 or 0.0.0.0:4000)")
	configPath := flag.String("config", "", "Path to optional YAML config file")
	flag.Parse()

	// --- Configuration ---
	var cfg config.IConfig
	var err error
	if *configPath != "" {
		cfg, err = config.NewYamlConfig(*configPath, logger)
		if err != nil {
			logger.Fatal("Failed to load YAML config", zap.String("path", *configPath), zap.Error(err))
		}
		// Override log level from config if specified
		if configLogLevel, configErr := cfg.LogLevel(); configErr == nil {
			level, err := zapcore.ParseLevel(configLogLevel)
			if err == nil {
				loggerConfig.Level.SetLevel(level)
				logger.Info("Set log level from config", zap.String("level", configLogLevel))
			} else {
				logger.Warn("Invalid log level in config", zap.String("level", configLogLevel), zap.Error(err))
			}
		}
		logger.Info("Loaded configuration from YAML", zap.String("path", *configPath))
	} else {
		// Use internal config if no path provided
		cfg = config.NewInternalConfig()
		// Configure InternalConfig defaults
		cfg.(*config.InternalConfig).ServerAddress = *listenAddr                             // Set listen address from flag
		cfg.(*config.InternalConfig).LogLevelValue = "info"                                  // Default log level
		cfg.(*config.InternalConfig).AuthorizationTypeValue = config.NotAuthorizedEverywhere // Example allows anon access
		// Setup default A2A Agent Card info in internal config
		cfg.(*config.InternalConfig).A2AAgentNameValue = "Gate4AI Demo Agent"
		cfg.(*config.InternalConfig).A2AAgentDescriptionValue = shared.PointerTo("An example A2A agent implementing various test scenarios based on text commands.")
		cfg.(*config.InternalConfig).A2AAgentVersionValue = "1.0.0" // Updated version
		cfg.(*config.InternalConfig).A2ADefaultInputModesValue = []string{"text", "file", "data"}
		cfg.(*config.InternalConfig).A2ADefaultOutputModesValue = []string{"text", "file", "data"}
		// Optionally add provider info
		cfg.(*config.InternalConfig).A2AProviderOrgValue = shared.PointerTo("Gate4AI Examples")
		cfg.(*config.InternalConfig).A2AProviderURLValue = shared.PointerTo("https://github.com/gate4ai")
		cfg.(*config.InternalConfig).A2ASkills = []a2aSchema.AgentSkill{
			{
				ID:          "scenario_runner",
				Name:        "A2A Scenario Runner",
				Description: shared.PointerTo("Runs different A2A test scenarios based on input text ('error_test', 'input_test', 'cancel_test', 'stream_test')."),
			},
		}

		logger.Info("Using default internal configuration")
	}

	// --- A2A Capability Setup ---
	// Use in-memory task store for this example
	taskStore := a2a.NewInMemoryTaskStore()
	// Get the agent handler function from the dedicated agent package
	agentHandler := agent.DemoAgentHandler

	// --- Server Setup ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actualListenAddr, _ := cfg.ListenAddr() // Get potentially overridden address
	logger.Info("Starting A2A Example Server", zap.String("address", actualListenAddr))

	// Configure server options using the builder pattern
	serverOptions := []server.ServerOption{
		// server.WithListenAddr(*listenAddr), // Listen address is now handled by config
		server.WithA2ACapability(taskStore, agentHandler), // Add the A2A capability with our store and specific agent handler
	}

	// Start the server
	errChan, startErr := server.Start(ctx, logger, cfg, serverOptions...)
	if startErr != nil {
		logger.Fatal("Failed to start server", zap.Error(startErr))
	}

	// --- Graceful Shutdown Handling ---
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-signalCh:
		logger.Info("Received shutdown signal", zap.String("signal", sig.String()))
		cancel() // Trigger context cancellation for server.Start's cleanup goroutine
	case err := <-errChan:
		// This channel receives errors *after* the listener has started or if it closes unexpectedly.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Server listener error", zap.Error(err))
		} else {
			logger.Info("Server listener closed.") // Could be due to graceful shutdown or other reasons
		}
		cancel() // Ensure context is cancelled if listener fails/closes
	case <-ctx.Done(): // Handle potential external cancellation (e.g., from tests)
		logger.Info("Server context cancelled externally")
	}

	// Wait briefly for graceful shutdown initiated by context cancellation in server.Start's goroutine
	shutdownGracePeriod := 5 * time.Second
	shutdownTimer := time.NewTimer(shutdownGracePeriod)
	defer shutdownTimer.Stop()

	// Wait for the error channel to be closed, indicating the listener has stopped.
	errChanClosed := make(chan struct{})
	go func() {
		_, ok := <-errChan // Wait until the channel is closed
		if !ok {
			close(errChanClosed)
		}
	}()

	select {
	case <-shutdownTimer.C:
		logger.Warn("Shutdown grace period timed out.")
	case <-errChanClosed:
		logger.Info("Server listener channel closed, shutdown should be complete.")
	case <-ctx.Done(): // Ensure we don't block indefinitely if context is cancelled again
		logger.Warn("Context cancelled again during shutdown wait.")
	}

	logger.Info("Server stopped")
}
