package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gate4ai/mcp/gateway"
	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Environment variable names
const (
	EnvDatabaseURL = "GATE4AI_DATABASE_URL"
	EnvConfigYAML  = "GATE4AI_CONFIG_YAML"
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

	configDB := flag.String("database-url", "", "PostgreSQL connection string for configuration")
	configYAML := flag.String("config-yaml", "", "Path to YAML configuration file")
	flag.Parse()

	if configDB != nil && *configDB != "" && configYAML != nil && *configYAML != "" {
		logger.Fatal("Cannot specify both database-url and config-yaml")
	}

	var cfg config.IConfig

	// Try database connection from environment or flags (priority)
	dbURL := os.Getenv(EnvDatabaseURL)
	if configDB != nil && *configDB != "" {
		dbURL = *configDB
	}

	// Try YAML config path from environment or flags (priority)
	yamlPath := os.Getenv(EnvConfigYAML)
	if configYAML != nil && *configYAML != "" {
		yamlPath = *configYAML
	}

	// Default YAML path if nothing else specified
	if yamlPath == "" && dbURL == "" {
		yamlPath = "config.yaml"
	}

	// Create config based on available sources
	if dbURL != "" {
		logger.Info("Loading configuration from database", zap.String("url", dbURL))
		cfg, err = config.NewDatabaseConfig(dbURL, logger)
		if err != nil {
			logger.Fatal("Failed to create database config", zap.Error(err))
		}
	} else if yamlPath != "" {
		logger.Info("Loading configuration from YAML file", zap.String("path", yamlPath))
		cfg, err = config.NewYamlConfig(yamlPath, logger)
		if err != nil {
			logger.Fatal("Failed to create YAML config", zap.Error(err))
		}
	} else {
		logger.Fatal("No configuration source specified")
	}
	defer cfg.Close()

	// Update logger level based on configuration
	logLevel, err := cfg.LogLevel()
	if err != nil {
		logger.Warn("Failed to get log level from config, using default", zap.Error(err))
	} else {
		var level zapcore.Level
		if err := level.UnmarshalText([]byte(logLevel)); err != nil {
			logger.Warn("Invalid log level in config, using default", zap.String("level", logLevel), zap.Error(err))
		} else {
			// Create a new logger with the configured level
			logerConfig.Level = zap.NewAtomicLevelAt(level)
			newLogger, err := logerConfig.Build()
			if err != nil {
				logger.Warn("Failed to create logger with new level, keeping default", zap.Error(err))
			} else {
				// Replace the logger
				logger.Info("Updating log level", zap.String("level", logLevel))
				logger = newLogger
			}
		}
	}

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handler for graceful shutdown
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signalCh
		logger.Info("Received termination signal")
		cancel()
	}()

	// Create and start the node
	node, err := gateway.Start(ctx, logger, cfg, "")
	if err != nil {
		logger.Fatal("Node failed to start", zap.Error(err))
	}

	// Wait for context cancellation (which happens when termination signal is received)
	<-ctx.Done()

	// Wait for the node to shut down, but not longer than 1 minute
	shutdownTimeout := 1 * time.Minute
	logger.Info("Waiting for node to shut down", zap.Duration("timeout", shutdownTimeout))

	if node.WaitForShutdown(shutdownTimeout) {
		logger.Info("Gateway service stopped gracefully")
	} else {
		logger.Warn("Gateway service shutdown timed out")
	}
}
