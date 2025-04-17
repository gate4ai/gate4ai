package env

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const DBComponentName = "database"

// DBEnv manages the PostgreSQL test container.
type DBEnv struct {
	BaseEnv // Embed BaseEnv for duration and default methods
	// --- Component-specific state ---
	container    testcontainers.Container
	dsn          string
	containerMux sync.RWMutex // Protect access to container and dsn
}

// NewDBEnv creates a new database environment component.
func NewDBEnv() *DBEnv {
	return &DBEnv{
		BaseEnv: BaseEnv{name: DBComponentName},
	}
}

// Configure for DBEnv doesn't need ports or have explicit dependencies from Configure phase.
func (e *DBEnv) Configure(envs *Envs) (dependencies []string, err error) {
	// No dependencies needed before Start for the DB itself
	return []string{}, nil
}

// Start launches the PostgreSQL container.
func (e *DBEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		req := testcontainers.ContainerRequest{
			Image:        "postgres:17-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "postgres",
				"POSTGRES_PASSWORD": "password",
				"POSTGRES_DB":       "gate4ai",
			},
			// Wait for port 5432 to be listening
			WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
		}

		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			// Check context cancellation
			if ctx.Err() != nil {
				resultChan <- fmt.Errorf("context cancelled during container start: %w", ctx.Err())
				return
			}
			resultChan <- fmt.Errorf("failed to start container: %w", err)
			return
		}

		// Get connection details
		host, err := container.Host(ctx)
		if err != nil {
			container.Terminate(context.Background()) // Cleanup on error
			resultChan <- fmt.Errorf("failed to get container host: %w", err)
			return
		}
		mappedPort, err := container.MappedPort(ctx, "5432")
		if err != nil {
			container.Terminate(context.Background()) // Cleanup on error
			resultChan <- fmt.Errorf("failed to get mapped port: %w", err)
			return
		}

		dsn := fmt.Sprintf("postgresql://postgres:password@%s:%s/gate4ai?sslmode=disable", host, mappedPort.Port())

		// Store state safely
		e.containerMux.Lock()
		e.container = container
		e.dsn = dsn
		e.containerMux.Unlock()

		os.Setenv("GATE4AI_DATABASE_URL", dsn) // Set env var for external tools (like Prisma CLI)

		log.Printf("PostgreSQL container started, DSN: %s", dsn)
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop terminates the PostgreSQL container.
func (e *DBEnv) Stop() error {
	e.containerMux.Lock()
	container := e.container
	e.containerMux.Unlock()

	if container != nil {
		log.Printf("Stopping component %s container...", e.Name())
		// Use a background context for termination during cleanup
		if err := container.Terminate(context.Background()); err != nil {
			return fmt.Errorf("failed to stop %s container: %w", e.Name(), err)
		}
		e.containerMux.Lock()
		e.container = nil // Mark as stopped
		e.containerMux.Unlock()
	}
	return nil
}

// URL returns the DSN (connection string) for the database.
func (e *DBEnv) URL() string {
	e.containerMux.RLock()
	defer e.containerMux.RUnlock()
	return e.dsn
}

// GetDetails for DBEnv returns nil, the URL is the primary detail.
// func (e *DBEnv) GetDetails() interface{} { return nil } // Uses BaseEnv default
