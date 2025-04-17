package env

import (
	"context"
	"fmt"
	"log"
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

	log.Printf("[%s] Configuring component. Intends to run on internal URL: %s", e.Name(), e.url)

	// Depends on migrations being done before starting the server process
	// Also needs the database URL available. DB settings should also be applied.
	log.Printf("[%s] Declaring dependencies: %v", e.Name(), []string{PrismaComponentName, DBSettingsComponentName, DBComponentName})
	return []string{PrismaComponentName, DBSettingsComponentName, DBComponentName}, nil
}

// Start builds and runs the Nuxt portal server.
func (e *PortalServerEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		logPrefix := fmt.Sprintf("[%s] ", e.Name())
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

		e.mux.RLock()
		port := e.port
		intendedURL := e.url
		e.mux.RUnlock()

		if port == 0 {
			err := fmt.Errorf("%sport not allocated in Configure phase", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sUsing port: %d", logPrefix, port)

		log.Printf("%sFetching database URL...", logPrefix)
		databaseURL := envs.GetURL(DBComponentName)
		if databaseURL == "" {
			err := fmt.Errorf("%sdatabase URL not available", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sDatabase URL obtained.", logPrefix)

		// Server context for managing the process lifecycle
		serverCtx, cancel := context.WithCancel(context.Background()) // Use background, manage via Stop()

		// Set up environment variables for the server
		jwtSecret := "a-secure-test-secret-key-for-go-tests-needs-to-be-at-least-32-chars-long"
		nodeEnv := "production" // Build and run in production mode for tests
		log.Printf("%sSetting environment variables (PORT=%d, HOST=localhost, NODE_ENV=%s)...", logPrefix, port, nodeEnv)

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
		log.Printf("%sUsing portal directory: %s", logPrefix, portalDir)

		// --- Build Step ---
		log.Printf("%sBuilding Nuxt app in %s mode...", logPrefix, nodeEnv)
		buildCmd := exec.CommandContext(ctx, "npm", "run", "build") // Use parent ctx for build timeout
		buildCmd.Dir = portalDir
		buildCmd.Env = serverEnv
		buildStartTime := time.Now()
		buildOutput, err := buildCmd.CombinedOutput()
		buildDuration := time.Since(buildStartTime)
		if err != nil {
			log.Printf("%sERROR: Build failed after %s. Output:\n%s", logPrefix, buildDuration, string(buildOutput))
			cancel() // Cancel server context
			resultChan <- fmt.Errorf("%sfailed to build portal: %w", logPrefix, err)
			return
		}
		if ctx.Err() != nil { // Check if parent context was cancelled during build
			log.Printf("%sBuild cancelled.", logPrefix)
			cancel()
			resultChan <- ctx.Err()
			return
		}
		log.Printf("%sBuild successful in %s.", logPrefix, buildDuration)

		// --- Start Step ---
		log.Printf("%sStarting Nuxt server process (npm run preview) on port %d...", logPrefix, port)
		cmd := exec.CommandContext(serverCtx, "npm", "run", "preview", "--", "--port", fmt.Sprintf("%d", port), "--host", "localhost")
		cmd.Dir = portalDir
		cmd.Env = serverEnv
		// Pipe output for debugging, could capture if needed
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Create process group for proper termination

		startStartTime := time.Now()
		err = cmd.Start()
		if err != nil {
			log.Printf("%sERROR: Failed to start portal server process: %v", logPrefix, err)
			cancel()
			resultChan <- fmt.Errorf("%sfailed to start portal server process: %w", logPrefix, err)
			return
		}
		log.Printf("%sServer process started (PID: %d). Waiting for readiness...", logPrefix, cmd.Process.Pid)

		// Store command and cancel func for Stop()
		e.mux.Lock()
		e.cmd = cmd
		e.cancelFunc = cancel
		e.mux.Unlock()

		// --- Wait for Readiness ---
		readinessURL := fmt.Sprintf("%s/api/status", intendedURL) // Assuming /api/status endpoint
		log.Printf("%sChecking readiness at %s...", logPrefix, readinessURL)
		// Use parent context for readiness check timeout
		waitCtx, waitCancel := context.WithTimeout(ctx, 120*time.Second) // Readiness timeout
		defer waitCancel()

		if err := waitForServer(waitCtx, readinessURL, 120*time.Second); err != nil {
			readinessDuration := time.Since(startStartTime)
			log.Printf("%sERROR: Readiness check failed after %s: %v", logPrefix, readinessDuration, err)
			e.Stop() // Attempt to stop the misbehaving process
			resultChan <- fmt.Errorf("%sserver readiness check failed: %w", logPrefix, err)
			return
		}
		readinessDuration := time.Since(startStartTime)
		log.Printf("%sServer is ready. Readiness check passed in %s.", logPrefix, readinessDuration)
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Stop terminates the portal server process group.
func (e *PortalServerEnv) Stop() error {
	logPrefix := fmt.Sprintf("[%s] ", e.Name())
	e.mux.Lock()
	cmd := e.cmd
	cancel := e.cancelFunc
	e.cmd = nil // Prevent double stopping
	e.cancelFunc = nil
	e.mux.Unlock()

	if cmd == nil || cmd.Process == nil {
		log.Printf("%sServer process already stopped or not started.", logPrefix)
		return nil
	}
	if cancel != nil {
		log.Printf("%sCancelling server context...", logPrefix)
		cancel() // Cancel the context first
	}

	pid := cmd.Process.Pid
	log.Printf("%sStopping server process group (PID: %d)...", logPrefix, pid)

	// Kill the entire process group using the negative PID
	pgid, err := syscall.Getpgid(pid)
	if err == nil {
		log.Printf("%sSending SIGTERM to process group %d...", logPrefix, pgid)
		// Send SIGTERM to the process group first
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			log.Printf("%sFailed to send SIGTERM to process group %d: %v. Trying SIGKILL.", logPrefix, pgid, err)
			// If SIGTERM fails, try SIGKILL
			_ = syscall.Kill(-pgid, syscall.SIGKILL) // Ignore SIGKILL error
		} else {
			log.Printf("%sSIGTERM sent to process group %d.", logPrefix, pgid)
			// Wait a short moment after SIGTERM before checking or forcefully killing
			time.Sleep(500 * time.Millisecond)
		}

	} else {
		log.Printf("%sFailed to get process group ID for PID %d: %v. Sending SIGTERM to process directly.", logPrefix, pid, err)
		// Fallback: Send SIGTERM directly to the process
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			log.Printf("%sFailed to send SIGTERM to process %d: %v. Trying SIGKILL.", logPrefix, pid, err)
			_ = cmd.Process.Kill() // Ignore error
		} else {
			log.Printf("%sSIGTERM sent to process %d.", logPrefix, pid)
			time.Sleep(500 * time.Millisecond)
		}

	}

	// Wait for the process to exit with a timeout
	waitDone := make(chan error, 1)
	go func() {
		log.Printf("%sWaiting for process %d to exit...", logPrefix, pid)
		waitDone <- cmd.Wait()
	}()

	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			// Ignore common exit errors after termination signals
			state, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok {
				// Handle case where Sys() is not syscall.WaitStatus (e.g., Windows)
				log.Printf("%sServer process %d exited with error, but could not get detailed status: %v", logPrefix, pid, waitErr)
				return fmt.Errorf("server process %d exit error: %w", pid, waitErr)
			}

			signaled := state.Signaled()
			exitStatus := state.ExitStatus()
			if signaled && (state.Signal() == syscall.SIGTERM || state.Signal() == syscall.SIGKILL) {
				log.Printf("%sProcess %d terminated as expected by signal %s.", logPrefix, pid, state.Signal())
			} else if !signaled && (exitStatus == 0 || exitStatus == -1 /* typically from signal */ || exitStatus == 1 /* common node exit code */) {
				log.Printf("%sProcess %d exited with status %d.", logPrefix, pid, exitStatus)
			} else {
				log.Printf("%sServer process %d exited with unexpected error: %v (State: %+v)", logPrefix, pid, waitErr, state)
				return fmt.Errorf("server process %d exit error: %w", pid, waitErr)
			}
		} else {
			log.Printf("%sServer process %d exited gracefully.", logPrefix, pid)
		}
	case <-time.After(5 * time.Second):
		log.Printf("%sTimeout waiting for server process %d to exit. Attempting forceful kill.", logPrefix, pid)
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
