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
	logPrefix := fmt.Sprintf("[%s] ", e.Name()) // Use consistent log prefix

	go func() {
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

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

		log.Printf("%sAttempting to start PostgreSQL container...", logPrefix)
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			log.Printf("%sERROR: Failed to start container: %v", logPrefix, err)
			// Check context cancellation
			if ctx.Err() != nil {
				resultChan <- fmt.Errorf("context cancelled during container start: %w", ctx.Err())
				return
			}
			resultChan <- fmt.Errorf("failed to start container: %w", err)
			return
		}
		log.Printf("%sPostgreSQL container started successfully.", logPrefix)

		// Get connection details
		log.Printf("%sRetrieving container host...", logPrefix)
		host, err := container.Host(ctx)
		if err != nil {
			log.Printf("%sERROR: Failed to get container host: %v", logPrefix, err)
			container.Terminate(context.Background()) // Cleanup on error
			resultChan <- fmt.Errorf("failed to get container host: %w", err)
			return
		}
		log.Printf("%sContainer host: %s", logPrefix, host)

		log.Printf("%sRetrieving mapped port...", logPrefix)
		mappedPort, err := container.MappedPort(ctx, "5432")
		if err != nil {
			log.Printf("%sERROR: Failed to get mapped port: %v", logPrefix, err)
			container.Terminate(context.Background()) // Cleanup on error
			resultChan <- fmt.Errorf("failed to get mapped port: %w", err)
			return
		}
		log.Printf("%sMapped port: %s", logPrefix, mappedPort.Port())

		dsn := fmt.Sprintf("postgresql://postgres:password@%s:%s/gate4ai?sslmode=disable", host, mappedPort.Port())

		// Store state safely
		log.Printf("%sStoring container and DSN...", logPrefix)
		e.containerMux.Lock()
		e.container = container
		e.dsn = dsn
		e.containerMux.Unlock()
		log.Printf("%sState stored.", logPrefix)

		log.Printf("%sSetting GATE4AI_DATABASE_URL environment variable...", logPrefix)
		os.Setenv("GATE4AI_DATABASE_URL", dsn) // Set env var for external tools (like Prisma CLI)
		log.Printf("%sEnvironment variable set.", logPrefix)

		log.Printf("%sComponent started successfully. DSN: %s", logPrefix, dsn)
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
		log.Printf("[%s] Stopping component container...", e.Name())
		// Use a background context for termination during cleanup
		if err := container.Terminate(context.Background()); err != nil {
			log.Printf("[%s] ERROR stopping container: %v", e.Name(), err)
			return fmt.Errorf("failed to stop %s container: %w", e.Name(), err)
		}
		log.Printf("[%s] Container stopped.", e.Name())
		e.containerMux.Lock()
		e.container = nil // Mark as stopped
		e.containerMux.Unlock()
	} else {
		log.Printf("[%s] Container already stopped or not started.", e.Name())
	}
	return nil
}

// URL returns the DSN (connection string) for the database.
func (e *DBEnv) URL() string {
	e.containerMux.RLock()
	defer e.containerMux.RUnlock()
	return e.dsn
}
