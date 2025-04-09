package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/gateway/client"
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// findAvailablePort returns an available port number
func FindAvailablePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port, nil
}

func FindAvailablePort3() (int, int, int, error) {
	ports := make([]int, 3)
	for i := 0; i < 3; i++ {
		port, err := FindAvailablePort()
		if err != nil {
			return 0, 0, 0, err
		}
		ports[i] = port
	}
	return ports[0], ports[1], ports[2], nil
}

// Run executes the gateway tools test
func GetToolsList(serverURL string, key string, logger *zap.Logger) ([]schema.Tool, error) {
	ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelTimeout()

	resultChan := make(chan client.GetToolsResult, 1)
	go func() {
		c, err := client.New(serverURL, serverURL, logger)
		if err != nil {
			resultChan <- client.GetToolsResult{Err: fmt.Errorf("failed to create client: %w", err)}
			return
		}
		session := c.NewSession(ctxTimeout, http.DefaultClient, key)
		defer session.Close()

		r := <-session.GetTools(ctxTimeout)
		resultChan <- r
	}()

	select {
	case result := <-resultChan:
		return result.Tools, result.Err
	case <-ctxTimeout.Done():
		return nil, ctxTimeout.Err()
	}
}
