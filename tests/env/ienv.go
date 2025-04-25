package env

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Environment represents a component of the test environment (e.g., database, server, setup task).
// Components are responsible for managing their own state (URL, details, etc.).
type Environment interface {
	// Name returns the unique name of the environment component. Used for dependency management and lookup.
	Name() string

	// Configure is called synchronously for all components before any Start methods are called.
	// It allows components to perform initial setup, like allocating ports via envs.GetFreePort(),
	// determining their intended URL, and performing quick configuration checks.
	// It MUST return the names of the components it depends on (which must have successfully completed Configure before this component's Start can be called).
	Configure(envs *Envs) (dependencies []string, err error)

	// Start is called asynchronously after Configure is complete for this component and all its dependencies.
	// It starts the main process (container, server, etc.).
	// If data from dependencies is needed (e.g., URL), the component should call envs.GetURL(dependencyName) or envs.GetDetails(dependencyName).
	// Note: A dependency's Start method might not have completed yet when its URL is requested.
	// The returned channel should send exactly one error (or nil for success) and then be closed.
	Start(ctx context.Context, envs *Envs) <-chan error

	// Stop gracefully stops the environment component.
	Stop() error

	// URL returns the accessible URL for the component.
	// It should return a valid URL after Configure (intended) or Start (final), or an empty string if not applicable or not yet known.
	URL() string

	// GetDetails returns component-specific details needed by tests (e.g., *playwright.Playwright), after the component has started.
	// Returns nil if no specific details are exposed or if Start hasn't completed successfully.
	GetDetails() interface{}

	// GetStartDuration returns the measured time it took for the Start method to complete successfully.
	GetStartDuration() time.Duration
	SetStartDuration(d time.Duration)
}

// BaseEnv provides a minimal base for Environment implementations.
// It stores the start duration and provides default empty/zero implementations for methods.
// Components embedding BaseEnv should override methods like Configure, Start, Stop, URL, GetDetails as needed
// and add their own fields for state management (e.g., intendedURL, finalURL, details, container instances).
type BaseEnv struct {
	// startDuration is measured and set by the Envs orchestrator after a successful Start.
	startDuration time.Duration
	// name is typically set by the constructor of the embedding type.
	name string
}

// Name returns the component's name.
func (b *BaseEnv) Name() string {
	return b.name
}

// Configure provides a default implementation returning no dependencies and no error.
// Components requiring configuration or dependencies MUST override this.
func (b *BaseEnv) Configure(envs *Envs) (dependencies []string, err error) {
	return []string{}, nil
}

// Start provides a default implementation that succeeds immediately.
// Components that need to start a process MUST override this.
func (b *BaseEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)
	go func() {
		resultChan <- nil
		close(resultChan)
	}()
	return resultChan
}

// Stop provides a default no-op implementation.
// Components managing resources (containers, processes) MUST override this.
func (b *BaseEnv) Stop() error {
	return nil
}

// URL provides a default implementation returning an empty string.
// Components with a URL MUST override this.
func (b *BaseEnv) URL() string {
	return ""
}

// GetDetails provides a default implementation returning nil.
// Components exposing details MUST override this.
func (b *BaseEnv) GetDetails() interface{} {
	return nil
}

// GetStartDuration returns the duration measured by the orchestrator.
func (b *BaseEnv) GetStartDuration() time.Duration {
	return b.startDuration
}

// setStartDuration allows the orchestrator (Envs) to set the duration.
// This is intentionally unexported but accessible within the same package.
func (b *BaseEnv) SetStartDuration(d time.Duration) {
	b.startDuration = d
}

func waitForServer(ctx context.Context, url string, timeout time.Duration) error {
	log.Printf("Waiting for server to become available at %s (timeout %s)...", url, timeout)
	startTime := time.Now()

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond) // Check every 500ms
	defer ticker.Stop()

	httpClient := &http.Client{
		Timeout: 2 * time.Second, // Short timeout for individual requests
		Transport: &http.Transport{
			DisableKeepAlives: true, // Avoid reusing connections during startup checks
		},
	}

	for {
		select {
		case <-checkCtx.Done():
			return fmt.Errorf("timed out waiting for server at %s after %s: %w", url, time.Since(startTime), checkCtx.Err())
		case <-ticker.C:
			req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, url, nil)
			if err != nil {
				// Should not happen with valid URL
				log.Printf("Error creating request for %s (will retry): %v", url, err)
				continue
			}

			resp, err := httpClient.Do(req)
			if err == nil {
				resp.Body.Close() // Close body immediately
				if resp.StatusCode == http.StatusOK {
					log.Printf("Server at %s is ready (status %d).", url, resp.StatusCode)
					// Optional: Add a small grace period after readiness?
					// time.Sleep(200 * time.Millisecond)
					return nil // Success!
				}
				// Log non-200 status but continue waiting
				log.Printf("Server at %s returned status %d, waiting...", url, resp.StatusCode)
			} else {
				// Log connection errors but continue waiting
				log.Printf("Failed to connect to server at %s (will retry): %v", url, err)
			}
		}
	}
}
