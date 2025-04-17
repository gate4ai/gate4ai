package tests

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/gateway/clients/mcpClient"
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

	resultChan := make(chan mcpClient.GetToolsResult, 1)
	go func() {
		c, err := mcpClient.New(serverURL, serverURL, logger)
		if err != nil {
			resultChan <- mcpClient.GetToolsResult{Err: fmt.Errorf("failed to create client: %w", err)}
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

// updateSetting updates a setting in the database
func updateSetting(key string, value interface{}) error {
	// Connect to the database
	db, err := sql.Open("postgres", DATABASE_URL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Marshal the value to JSON
	var valueJSON []byte
	switch v := value.(type) {
	case string:
		valueJSON = []byte(fmt.Sprintf("%q", v))
	case json.RawMessage:
		valueJSON = v
	default:
		valueJSON, err = json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value to JSON: %w", err)
		}
	}

	// Check if the setting exists
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM "Settings" WHERE key = $1`, key).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check if setting exists: %w", err)
	}

	if count > 0 {
		// Update existing setting
		_, err = db.Exec(`UPDATE "Settings" SET value = $1 WHERE key = $2`, valueJSON, key)
		if err != nil {
			return fmt.Errorf("failed to update setting: %w", err)
		}
	} else {
		// Insert new setting with minimal required fields
		_, err = db.Exec(`INSERT INTO "Settings" (key, "group", name, description, value, frontend) VALUES ($1, 'test', $1, $1, $2, false)`,
			key, valueJSON)
		if err != nil {
			return fmt.Errorf("failed to insert setting: %w", err)
		}
	}

	return nil
}
