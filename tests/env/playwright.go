package env

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/playwright-community/playwright-go"
)

const PlaywrightComponentName = "playwright"

// PlaywrightEnv manages the Playwright instance.
type PlaywrightEnv struct {
	BaseEnv // Embed BaseEnv for duration and default methods
	// --- Component-specific state ---
	pwInstance *playwright.Playwright
	pwMux      sync.RWMutex
}

// NewPlaywrightEnv creates a new Playwright environment component.
func NewPlaywrightEnv() *PlaywrightEnv {
	return &PlaywrightEnv{
		BaseEnv: BaseEnv{name: PlaywrightComponentName},
	}
}

// Configure - Playwright requires no specific configuration or dependencies at this phase.
func (e *PlaywrightEnv) Configure(envs *Envs) (dependencies []string, err error) {
	return []string{}, nil
}

// Start initializes the Playwright instance.
func (e *PlaywrightEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)
	logPrefix := fmt.Sprintf("[%s] ", e.Name()) // Use consistent log prefix

	go func() {
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

		// playwright.Run() can be blocking during browser downloads etc.
		// We rely on the parent context potentially cancelling if it takes too long.
		log.Printf("%sAttempting to run playwright.Run()...", logPrefix)
		pw, err := playwright.Run() // Consider RunOptions{...} if needed
		if err != nil {
			log.Printf("%sERROR: Failed to run playwright: %v", logPrefix, err)
			// Check context cancellation
			if ctx.Err() != nil {
				resultChan <- fmt.Errorf("context cancelled during playwright start: %w", ctx.Err())
				return
			}
			resultChan <- fmt.Errorf("failed to run playwright: %w", err)
			return
		}
		log.Printf("%splaywright.Run() completed successfully.", logPrefix)

		log.Printf("%sStoring Playwright instance...", logPrefix)
		e.pwMux.Lock()
		e.pwInstance = pw
		e.pwMux.Unlock()
		log.Printf("%sPlaywright instance stored.", logPrefix)

		log.Printf("%sComponent started successfully.", logPrefix)
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop closes the Playwright connection.
func (e *PlaywrightEnv) Stop() error {
	e.pwMux.Lock()
	pw := e.pwInstance
	e.pwMux.Unlock()

	if pw != nil {
		log.Printf("[%s] Stopping component...", e.Name())
		if err := pw.Stop(); err != nil {
			log.Printf("[%s] ERROR stopping component: %v", e.Name(), err)
			return fmt.Errorf("failed to stop %s: %w", e.Name(), err)
		}
		log.Printf("[%s] Component stopped.", e.Name())
		e.pwMux.Lock()
		e.pwInstance = nil // Mark as stopped
		e.pwMux.Unlock()
	} else {
		log.Printf("[%s] Component already stopped or not started.", e.Name())
	}
	return nil
}

// URL for Playwright is not applicable.
// func (e *PlaywrightEnv) URL() string { return "" } // Uses BaseEnv default

// GetDetails returns the Playwright instance.
func (e *PlaywrightEnv) GetDetails() interface{} {
	e.pwMux.RLock()
	defer e.pwMux.RUnlock()
	return e.pwInstance
}
