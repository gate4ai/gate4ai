package env

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const PortalComponentName = "portal"

// PortalServerEnv manages the Nuxt portal server process.
type PortalServerEnv struct {
	BaseEnv
	port       int
	url        string // Intended/Actual URL
	cmd        *exec.Cmd
	cancelFunc context.CancelFunc
	mux        sync.RWMutex
}

// NewPortalServerEnv creates a new portal server component.
func NewPortalServerEnv() *PortalServerEnv {
	return &PortalServerEnv{
		BaseEnv: BaseEnv{name: PortalComponentName},
	}
}

// Configure allocates a port for the portal and declares dependencies.
func (e *PortalServerEnv) Configure(envs *Envs) (dependencies []string, err error) {
	port, err := envs.GetFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to get free port for portal: %w", err)
	}

	e.mux.Lock()
	e.port = port
	e.url = fmt.Sprintf("http://localhost:%d", port) // This is the internal URL
	e.mux.Unlock()

	log.Printf("%s: Intends to run on internal URL: %s", e.Name(), e.url)

	// Depends on migrations being done before starting the server process
	// Also needs the database URL available.
	return []string{PrismaComponentName, DBSettingsComponentName, DBComponentName}, nil
}

// Start builds and runs the Nuxt portal server.
func (e *PortalServerEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		e.mux.RLock()
		port := e.port
		intendedURL := e.url
		e.mux.RUnlock()

		if port == 0 {
			resultChan <- fmt.Errorf("%s: port not allocated in Configure phase", e.Name())
			return
		}

		databaseURL := envs.GetURL(DBComponentName)
		if databaseURL == "" {
			resultChan <- fmt.Errorf("%s: database URL not available", e.Name())
			return
		}

		// Server context for managing the process lifecycle
		serverCtx, cancel := context.WithCancel(context.Background()) // Use background, manage via Stop()

		// Set up environment variables for the server
		// Ensure JWT secret is strong enough for production build mode
		jwtSecret := "a-secure-test-secret-key-for-go-tests-needs-to-be-at-least-32-chars-long"
		nodeEnv := "production" // Build and run in production mode for tests

		serverEnv := append(os.Environ(),
			fmt.Sprintf("PORT=%d", port),
			fmt.Sprintf("NUXT_PORT=%d", port),   // Nuxt might use NUXT_PORT
			fmt.Sprintf("HOST=%s", "localhost"), // Explicitly bind to localhost
			fmt.Sprintf("NUXT_HOST=%s", "localhost"),
			fmt.Sprintf("GATE4AI_DATABASE_URL=%s", databaseURL),
			fmt.Sprintf("NUXT_JWT_SECRET=%s", jwtSecret),
			fmt.Sprintf("NODE_ENV=%s", nodeEnv),
		)

		portalDir := filepath.Join(TestConfigWorkspaceFolder, "portal")

		// --- Build Step ---
		log.Printf("%s: Building Nuxt app in %s mode...", e.Name(), nodeEnv)
		buildCmd := exec.CommandContext(ctx, "npm", "run", "build") // Use parent ctx for build timeout
		buildCmd.Dir = portalDir
		buildCmd.Env = serverEnv
		buildOutput, err := buildCmd.CombinedOutput()
		if err != nil {
			log.Printf("%s: Build failed. Output:\n%s", e.Name(), string(buildOutput))
			cancel() // Cancel server context
			resultChan <- fmt.Errorf("%s: failed to build portal: %w", e.Name(), err)
			return
		}
		if ctx.Err() != nil { // Check if parent context was cancelled during build
			log.Printf("%s: Build cancelled.", e.Name())
			cancel()
			resultChan <- ctx.Err()
			return
		}
		log.Printf("%s: Build successful.", e.Name())

		// --- Start Step ---
		log.Printf("%s: Starting Nuxt server process (npm run preview) on port %d...", e.Name(), port)
		cmd := exec.CommandContext(serverCtx, "npm", "run", "preview", "--", "--port", fmt.Sprintf("%d", port), "--host", "localhost")
		cmd.Dir = portalDir
		cmd.Env = serverEnv
		// Pipe output for debugging, could capture if needed
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Create process group for proper termination

		err = cmd.Start()
		if err != nil {
			cancel()
			resultChan <- fmt.Errorf("%s: failed to start portal server process: %w", e.Name(), err)
			return
		}

		// Store command and cancel func for Stop()
		e.mux.Lock()
		e.cmd = cmd
		e.cancelFunc = cancel
		e.mux.Unlock()

		log.Printf("%s: Server process started (PID: %d). Waiting for readiness...", e.Name(), cmd.Process.Pid)

		// --- Wait for Readiness ---
		// Use the internal URL and a known health/status endpoint
		readinessURL := fmt.Sprintf("%s/api/status", intendedURL)
		// Use parent context for readiness check timeout
		waitCtx, waitCancel := context.WithTimeout(ctx, 120*time.Second) // Readiness timeout
		defer waitCancel()

		if err := waitForServer(waitCtx, readinessURL, 120*time.Second); err != nil {
			log.Printf("%s: Readiness check failed: %v", e.Name(), err)
			e.Stop() // Attempt to stop the misbehaving process
			resultChan <- fmt.Errorf("%s: server readiness check failed: %w", e.Name(), err)
			return
		}

		log.Printf("%s: Server is ready.", e.Name())
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop terminates the portal server process group.
func (e *PortalServerEnv) Stop() error {
	e.mux.Lock()
	cmd := e.cmd
	cancel := e.cancelFunc
	e.cmd = nil // Prevent double stopping
	e.cancelFunc = nil
	e.mux.Unlock()

	if cmd == nil || cmd.Process == nil {
		log.Printf("%s: Server process already stopped or not started.", e.Name())
		return nil
	}
	if cancel != nil {
		cancel() // Cancel the context first
	}

	pid := cmd.Process.Pid
	log.Printf("%s: Stopping server process group (PID: %d)...", e.Name(), pid)

	// Kill the entire process group using the negative PID
	pgid, err := syscall.Getpgid(pid)
	if err == nil {
		// Send SIGTERM to the process group first
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			log.Printf("%s: Failed to send SIGTERM to process group %d: %v. Trying SIGKILL.", e.Name(), pgid, err)
			// If SIGTERM fails, try SIGKILL
			_ = syscall.Kill(-pgid, syscall.SIGKILL) // Ignore SIGKILL error
		} else {
			// Wait a short moment after SIGTERM before checking or forcefully killing
			time.Sleep(500 * time.Millisecond)
		}

	} else {
		log.Printf("%s: Failed to get process group ID for PID %d: %v. Sending SIGTERM to process directly.", e.Name(), pid, err)
		// Fallback: Send SIGTERM directly to the process
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			log.Printf("%s: Failed to send SIGTERM to process %d: %v. Trying SIGKILL.", e.Name(), pid, err)
			_ = cmd.Process.Kill() // Ignore error
		} else {
			time.Sleep(500 * time.Millisecond)
		}

	}

	// Wait for the process to exit with a timeout
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			// Ignore common exit errors after termination signals
			state, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
			signaled := state.Signaled()
			exitStatus := state.ExitStatus()
			if signaled && (state.Signal() == syscall.SIGTERM || state.Signal() == syscall.SIGKILL) {
				log.Printf("%s: Process %d terminated as expected by signal %s.", e.Name(), pid, state.Signal())
			} else if !signaled && (exitStatus == 0 || exitStatus == -1 /* typically from signal */ || exitStatus == 1 /* common node exit code */) {
				log.Printf("%s: Process %d exited with status %d.", e.Name(), pid, exitStatus)
			} else {
				log.Printf("%s: Server process %d exited with unexpected error: %v (State: %+v)", e.Name(), pid, waitErr, state)
				return fmt.Errorf("server process %d exit error: %w", pid, waitErr)
			}
		} else {
			log.Printf("%s: Server process %d exited gracefully.", e.Name(), pid)
		}
	case <-time.After(5 * time.Second):
		log.Printf("%s: Timeout waiting for server process %d to exit. Attempting forceful kill.", e.Name(), pid)
		// Force kill if still running after timeout
		if pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
		}
		return fmt.Errorf("timeout waiting for portal server process %d to stop", pid)
	}
	return nil
}

// URL returns the internal URL the portal server listens on.
func (e *PortalServerEnv) URL() string {
	e.mux.RLock()
	defer e.mux.RUnlock()
	return e.url
}

// GetDetails returns nil for the portal server.
// func (e *PortalServerEnv) GetDetails() interface{} { return nil } // Uses BaseEnv default

// Helper function if needed specifically for portal server
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
