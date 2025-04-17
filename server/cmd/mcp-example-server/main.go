package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gate4ai/gate4ai/server"
	"github.com/gate4ai/gate4ai/server/cmd/mcp-example-server/exampleCapability"
	"github.com/gate4ai/gate4ai/shared/config"
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

	overwriteListenAddr := ""
	if *port != 0 {
		overwriteListenAddr = fmt.Sprintf(":%d", *port)
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

	serverOptions := exampleCapability.BuildOptions(logger)
	if overwriteListenAddr != "" {
		serverOptions = append(serverOptions, server.WithListenAddr(overwriteListenAddr))
	}

	errChan, err := server.Start(ctx, logger, cfg, serverOptions...)
	if err != nil {
		logger.Fatal("Failed to start server", zap.Error(err))
	}

	// --- Wait for Termination ---
	select {
	case startErr := <-errChan:
		if startErr != nil {
			logger.Fatal("Server encountered an error", zap.Error(startErr))
		} else {
			logger.Info("Server shutdown initiated cleanly")
		}
	case <-ctx.Done(): // Handle external cancellation
		logger.Info("Server context done")
	}

	logger.Info("Server stopped")
}
