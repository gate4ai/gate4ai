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
	logPrefix := fmt.Sprintf("[%s] ", e.Name()) // Use consistent log prefix

	go func() {
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

		req := testcontainers.ContainerRequest{
			Image:        "mailhog/mailhog:latest",
			ExposedPorts: []string{"1025/tcp", "8025/tcp"}, // SMTP and API ports
			// Wait for the API endpoint to be ready
			WaitingFor: wait.ForHTTP("/").WithPort("8025/tcp").WithStartupTimeout(60 * time.Second),
		}

		log.Printf("%sAttempting to start MailHog container...", logPrefix)
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			log.Printf("%sERROR: Failed to start mailhog container: %v", logPrefix, err)
			if ctx.Err() != nil {
				resultChan <- fmt.Errorf("context cancelled during container start: %w", ctx.Err())
				return
			}
			resultChan <- fmt.Errorf("failed to start mailhog container: %w", err)
			return
		}
		log.Printf("%sMailHog container started successfully.", logPrefix)

		// Get connection details
		log.Printf("%sRetrieving container host...", logPrefix)
		host, err := container.Host(ctx)
		if err != nil {
			log.Printf("%sERROR: Failed to get mailhog host: %v", logPrefix, err)
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get mailhog host: %w", err)
			return
		}
		log.Printf("%sContainer host: %s", logPrefix, host)

		log.Printf("%sRetrieving mapped SMTP port (1025)...", logPrefix)
		smtpPort, err := container.MappedPort(ctx, "1025")
		if err != nil {
			log.Printf("%sERROR: Failed to get mailhog smtp port: %v", logPrefix, err)
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get mailhog smtp port: %w", err)
			return
		}
		log.Printf("%sMapped SMTP port: %d", logPrefix, smtpPort.Int())

		log.Printf("%sRetrieving mapped API port (8025)...", logPrefix)
		apiPort, err := container.MappedPort(ctx, "8025")
		if err != nil {
			log.Printf("%sERROR: Failed to get mailhog api port: %v", logPrefix, err)
			container.Terminate(context.Background())
			resultChan <- fmt.Errorf("failed to get mailhog api port: %w", err)
			return
		}
		log.Printf("%sMapped API port: %s", logPrefix, apiPort.Port())

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
		log.Printf("%sAPI URL: %s, SMTP Details: %+v", logPrefix, apiURL, smtpDetails)

		// Store state safely
		log.Printf("%sStoring container and connection details...", logPrefix)
		e.containerMux.Lock()
		e.container = container
		e.apiURL = apiURL
		e.smtpDetails = smtpDetails
		e.containerMux.Unlock()
		log.Printf("%sState stored.", logPrefix)

		log.Printf("%sComponent started successfully. API: %s, SMTP: %s:%d", logPrefix, apiURL, host, smtpPort.Int())
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
		log.Printf("[%s] Stopping component container...", e.Name())
		if err := container.Terminate(context.Background()); err != nil {
			log.Printf("[%s] ERROR stopping container: %v", e.Name(), err)
			return fmt.Errorf("failed to stop %s container: %w", e.Name(), err)
		}
		log.Printf("[%s] Container stopped.", e.Name())
		e.containerMux.Lock()
		e.container = nil
		e.containerMux.Unlock()
	} else {
		log.Printf("[%s] Container already stopped or not started.", e.Name())
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
