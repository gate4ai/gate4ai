package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gate4ai/mcp/server"
	"github.com/gate4ai/mcp/server/cmd/mcp-example-server/capability"
	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	logerConfig := zap.NewProductionConfig()
	logerConfig.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := logerConfig.Build()
	if err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// Parse command-line arguments
	port := flag.Int("port", 0, "Port to run the server on")
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	cfg, err := config.NewYamlConfig(*configPath, logger)
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}
	if *port != 0 {
		cfg.SetListenAddr(fmt.Sprintf(":%d", *port))
	}

	// Create a context that cancels on SIGINT or SIGTERM
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signalCh
		logger.Info("Received shutdown signal, stopping server...")
		cancel()
	}()

	add, _ := cfg.ListenAddr()

	// Start the server
	logger.Info("Starting MCP example server",
		zap.String("address", add),
		zap.String("config", *configPath))

	toolsCapability, resourcesCapability, promptsCapability, completionCapability, err := server.StartServer(ctx, logger, cfg, "")
	if err != nil {
		logger.Fatal("Failed to start server", zap.Error(err))
	}

	capability.Add(toolsCapability, resourcesCapability, promptsCapability, completionCapability)

	<-ctx.Done()
	logger.Info("Server stopped")
}
