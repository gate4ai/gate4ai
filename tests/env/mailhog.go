package env

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const MailhogComponentName = "mailhog"

// SmtpServerDetails holds the connection details for the MailHog SMTP server.
type SmtpServerDetails struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Secure bool   `json:"secure"`
	Auth   struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	} `json:"auth"`
}

// MailhogEnv manages the MailHog test container.
type MailhogEnv struct {
	BaseEnv
	container    testcontainers.Container
	apiURL       string
	smtpDetails  SmtpServerDetails
	containerMux sync.RWMutex
}

// NewMailHogEnv creates a new MailHog environment component.
func NewMailHogEnv() *MailhogEnv {
	return &MailhogEnv{
		BaseEnv: BaseEnv{name: MailhogComponentName},
	}
}

// Configure - MailHog needs no specific configuration or dependencies at this phase.
func (e *MailhogEnv) Configure(envs *Envs) (dependencies []string, err error) {
	return []string{}, nil
}

// Start launches the MailHog container.
func (e *MailhogEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		req := testcontainers.ContainerRequest{
			Image:        "mailhog/mailhog:latest",
			ExposedPorts: []string{"1025/tcp", "8025/tcp"}, // SMTP and API ports
			// Wait for the API endpoint to be ready
			WaitingFor: wait.ForHTTP("/").WithPort("8025/tcp").WithStartupTimeout(60 * time.Second),
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
			resultChan <- fmt.Errorf("failed to start mailhog container: %w", err)
			return
		}

		// Get connection details
		host, err := container.Host(ctx)
		if err != nil {
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get mailhog host: %w", err)
			return
		}

		smtpPort, err := container.MappedPort(ctx, "1025")
		if err != nil {
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get mailhog smtp port: %w", err)
			return
		}

		apiPort, err := container.MappedPort(ctx, "8025")
		if err != nil {
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get mailhog api port: %w", err)
			return
		}

		apiURL := fmt.Sprintf("http://%s:%s", host, apiPort.Port())
		smtpDetails := SmtpServerDetails{
			Host:   host,
			Port:   smtpPort.Int(),
			Secure: false, // MailHog doesn't use TLS by default
			Auth: struct {
				User string `json:"user"`
				Pass string `json:"pass"`
			}{User: "", Pass: ""}, // MailHog doesn't use auth by default
		}

		// Store state safely
		e.containerMux.Lock()
		e.container = container
		e.apiURL = apiURL
		e.smtpDetails = smtpDetails
		e.containerMux.Unlock()

		log.Printf("MailHog container started, API: %s, SMTP: %s:%d", apiURL, host, smtpPort.Int())
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop terminates the MailHog container.
func (e *MailhogEnv) Stop() error {
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

// URL returns the MailHog API URL.
func (e *MailhogEnv) URL() string {
	e.containerMux.RLock()
	defer e.containerMux.RUnlock()
	return e.apiURL
}

// GetDetails returns the SMTP server connection details.
func (e *MailhogEnv) GetDetails() interface{} {
	e.containerMux.RLock()
	defer e.containerMux.RUnlock()
	// Return a copy to avoid external modification? For struct, copy is implicit.
	return e.smtpDetails
}
