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

const A2AServerComponentName = "a2a-server"

// A2AServerEnv manages the A2A test server container built from a Dockerfile.
type A2AServerEnv struct {
	BaseEnv
	container    testcontainers.Container
	serverURL    string
	containerMux sync.RWMutex
}

// NewA2AServerEnv creates a new A2A server environment component.
func NewA2AServerEnv() *A2AServerEnv {
	return &A2AServerEnv{
		BaseEnv: BaseEnv{name: A2AServerComponentName},
	}
}

// Configure - A2A server needs no specific configuration or dependencies at this phase.
func (e *A2AServerEnv) Configure(envs *Envs) (dependencies []string, err error) {
	return []string{}, nil
}

// Start builds and launches the A2A test server container.
func (e *A2AServerEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		// Define the build context relative to the tests directory
		contextPath := "../../.docs/a2a/samples/js"
		dockerfilePath := "a2a-coder.Dockerfile"

		req := testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    contextPath,
				Dockerfile: dockerfilePath,
			},
			ExposedPorts: []string{"41241/tcp"},
			// Wait for the specific port to be listening
			WaitingFor: wait.ForListeningPort("41241/tcp").WithStartupTimeout(120 * time.Second), // Longer timeout for build+start
		}

		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			if ctx.Err() != nil {
				resultChan <- fmt.Errorf("context cancelled during container start: %w", ctx.Err())
				return
			}
			resultChan <- fmt.Errorf("failed to start a2a container: %w", err)
			return
		}

		// Get connection details
		host, err := container.Host(ctx)
		if err != nil {
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get a2a container host: %w", err)
			return
		}

		mappedPort, err := container.MappedPort(ctx, "41241")
		if err != nil {
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get a2a mapped port: %w", err)
			return
		}

		serverURL := fmt.Sprintf("http://%s:%s", host, mappedPort.Port())

		// Store state safely
		e.containerMux.Lock()
		e.container = container
		e.serverURL = serverURL
		e.containerMux.Unlock()

		os.Setenv("GATE4AI_A2A_SERVER_URL", serverURL) // Set for compatibility if needed elsewhere

		log.Printf("A2A server container started, URL: %s", serverURL)
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop terminates the A2A server container.
func (e *A2AServerEnv) Stop() error {
	e.containerMux.Lock()
	container := e.container
	e.containerMux.Unlock()

	if container != nil {
		log.Printf("Stopping component %s container...", e.Name())
		if err := container.Terminate(context.Background()); err != nil {
			return fmt.Errorf("failed to stop %s container: %w", e.Name(), err)
		}
		e.containerMux.Lock()
		e.container = nil
		e.containerMux.Unlock()
	}
	return nil
}

// URL returns the A2A server URL.
func (e *A2AServerEnv) URL() string {
	e.containerMux.RLock()
	defer e.containerMux.RUnlock()
	return e.serverURL
}

// GetDetails for A2A server returns nil.
// func (e *A2AServerEnv) GetDetails() interface{} { return nil } // Uses BaseEnv default
