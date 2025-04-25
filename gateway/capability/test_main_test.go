package capability_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"testing"

	"github.com/gate4ai/gate4ai/gateway"
	"github.com/gate4ai/gate4ai/server"
	"github.com/gate4ai/gate4ai/server/cmd/mcp-example-server/exampleCapability"
	"github.com/gate4ai/gate4ai/shared/config"
	"github.com/gate4ai/gate4ai/tests"
	"go.uber.org/zap"
)

var (
	GW_URL string
	LOGGER *zap.Logger
)

func TestMain(m *testing.M) {
	var exitCode int
	defer func() {
		os.Exit(exitCode)
	}()
	ctx := context.Background()
	var err error
	LOGGER, err = zap.NewDevelopment()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}

	portForExampleServer1, err := tests.FindAvailablePort()
	if err != nil {
		log.Fatalf("Failed to find available port: %v", err)
	}
	portForExampleServer2, err := tests.FindAvailablePort()
	if err != nil {
		log.Fatalf("Failed to find available port: %v", err)
	}
	portForGateway, err := tests.FindAvailablePort()
	if err != nil {
		log.Fatalf("Failed to find available port: %v", err)
	}

	// run Example Server1
	cfg1 := config.NewInternalConfig()
	cfg1.UserKeyHashes[config.HashAPIKey("gateway")] = "gw"
	serverOptions1 := exampleCapability.BuildOptions(LOGGER.With(zap.String("s", "server1builder")))
	serverOptions1 = append(serverOptions1, server.WithListenAddr(fmt.Sprintf(":%d", portForExampleServer1)))
	_, err = server.Start(ctx, LOGGER.With(zap.String("s", "server1")), cfg1, serverOptions1...)
	if err != nil {
		LOGGER.Fatal("Failed to start server", zap.Error(err))
	}

	//run Example Server2
	cfg2 := config.NewInternalConfig()
	cfg2.UserKeyHashes[config.HashAPIKey("gateway")] = "gw"
	serverOptions2 := exampleCapability.BuildOptions(LOGGER.With(zap.String("s", "server2builder")))
	serverOptions2 = append(serverOptions2, server.WithListenAddr(fmt.Sprintf(":%d", portForExampleServer2)))
	_, err = server.Start(ctx, LOGGER.With(zap.String("s", "server2")), cfg2, serverOptions2...)
	if err != nil {
		LOGGER.Fatal("Failed to start server", zap.Error(err))
	}

	//run Gateway
	cfgGw := config.NewInternalConfig()
	cfgGw.UserKeyHashes[config.HashAPIKey("key-user0")] = "user0"
	cfgGw.UserKeyHashes[config.HashAPIKey("key-user1")] = "user1"
	cfgGw.UserKeyHashes[config.HashAPIKey("key-user2")] = "user2"
	cfgGw.UserKeyHashes[config.HashAPIKey("key-user3")] = "user3"
	cfgGw.UserSubscribes["user1"] = []string{"backend1"}
	cfgGw.UserSubscribes["user2"] = []string{"backend2"}
	cfgGw.UserSubscribes["user3"] = []string{"backend1", "backend2"}
	cfgGw.Backends["backend1"] = &config.Backend{URL: "http://localhost:" + strconv.Itoa(portForExampleServer1) + "/sse?key=gateway"}
	cfgGw.Backends["backend2"] = &config.Backend{URL: "http://localhost:" + strconv.Itoa(portForExampleServer2) + "/sse?key=gateway"}
	_, err = gateway.Start(ctx, LOGGER.With(zap.String("s", "gateway")), cfgGw, fmt.Sprintf(":%d", portForGateway))
	if err != nil {
		log.Fatalf("Failed to find available port: %v", err)
	}

	GW_URL = "http://localhost:" + strconv.Itoa(portForGateway) + "/sse"

	exitCode = m.Run()
}
