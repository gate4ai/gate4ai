package env

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Envs manages the lifecycle and state of the test environment components.
type Envs struct {
	components map[string]Environment
	portMu     sync.Mutex
	usedPorts  map[int]struct{}
}

// NewEnvs creates a new environment manager.
func NewEnvs() *Envs {
	return &Envs{
		components: make(map[string]Environment),
		usedPorts:  make(map[int]struct{}),
	}
}

// Register adds one or more environment components to the manager.
// Should be called before Execute. Panics on duplicate name.
func (e *Envs) Register(envs ...Environment) {
	if e.components == nil {
		e.components = make(map[string]Environment)
	}
	for _, env := range envs {
		name := env.Name()
		if _, exists := e.components[name]; exists {
			panic(fmt.Sprintf("environment component with name '%s' already registered", name))
		}
		e.components[name] = env
		log.Printf("Registered component: %s", name)
	}
}

// GetFreePort finds and reserves an available TCP port.
func (e *Envs) GetFreePort() (int, error) {
	e.portMu.Lock()
	defer e.portMu.Unlock()

	if e.usedPorts == nil {
		e.usedPorts = make(map[int]struct{})
	}

	// Try up to 100 times to find a port
	for i := 0; i < 100; i++ {
		listener, err := net.Listen("tcp", ":0") // :0 requests a free port from the OS
		if err != nil {
			// If Listen fails, OS might be temporarily out of ports or having issues
			log.Printf("Warning: net.Listen(\":0\") failed (attempt %d): %v", i+1, err)
			time.Sleep(50 * time.Millisecond) // Small delay before retrying
			continue
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close() // Close immediately, we just needed the port number

		// Check if we haven't already allocated this port in this run
		if _, used := e.usedPorts[port]; !used {
			e.usedPorts[port] = struct{}{}
			log.Printf("Allocated port: %d", port)
			return port, nil
		}
		// If port was already used by us, loop again to get a different one
		log.Printf("Port %d already allocated, trying again...", port)
	}

	return 0, fmt.Errorf("failed to find an available free port after multiple attempts")
}

// Execute orchestrates the configuration and startup of all registered components.
func (e *Envs) Execute(ctx context.Context) error {
	startTime := time.Now()
	log.Println("Starting environment setup...")

	if len(e.components) == 0 {
		log.Println("No components registered, setup complete.")
		return nil
	}

	// --- Phase 1: Configure Components and Build Dependency Graph ---
	log.Println("Executing Configure phase...")
	dependenciesMap := make(map[string][]string)              // component name -> list of dependency names
	configureGroup, configureCtx := errgroup.WithContext(ctx) // Allow early exit on configure error

	for _, env := range e.components {
		// Capture loop variables for the goroutine
		currentEnv := env
		currentName := currentEnv.Name() // Capture name for logging inside goroutine
		configureGroup.Go(func() error {
			select {
			case <-configureCtx.Done(): // Check if another configure task failed
				log.Printf("[%s] Configure cancelled due to prior error: %v", currentName, configureCtx.Err())
				return configureCtx.Err()
			default:
				log.Printf("[%s] Configuring component...", currentName)
				deps, err := currentEnv.Configure(e) // Pass Envs for potential port allocation
				if err != nil {
					log.Printf("[%s] ERROR: Failed to configure component: %v", currentName, err)
					return fmt.Errorf("configure %s failed: %w", currentName, err)
				}
				// Safely store dependencies
				// This access is safe because each goroutine writes to a unique key derived from its component
				dependenciesMap[currentName] = deps // Store dependencies returned by Configure
				log.Printf("[%s] Component configured successfully, dependencies: %v", currentName, deps)
				return nil
			}
		})
	}

	if err := configureGroup.Wait(); err != nil {
		log.Printf("Environment setup failed during Configure phase: %v", err)
		// No components started yet, no cleanup needed.
		return err // Return the first configuration error
	}
	log.Println("Configure phase completed successfully.")

	// --- Build Graph & Check Dependencies ---
	log.Println("Building dependency graph...")
	depGraph := make(map[string][]string) // dependency name -> list of components that depend on it
	depCount := make(map[string]int)      // component name -> number of unmet dependencies
	initialStarters := []string{}         // Names of components with no dependencies

	for name := range e.components {
		deps := dependenciesMap[name]
		depCount[name] = len(deps)

		if depCount[name] == 0 {
			initialStarters = append(initialStarters, name)
		} else {
			for _, depName := range deps {
				if _, exists := e.components[depName]; !exists {
					err := fmt.Errorf("component '%s' configured dependency '%s' which is not registered", name, depName)
					log.Printf("ERROR: %v", err)
					return err // Fail fast on unknown dependency
				}
				depGraph[depName] = append(depGraph[depName], name)
			}
		}
	}
	log.Printf("Initial starters (no dependencies): %v", initialStarters)

	log.Println("Checking for dependency cycles...")
	// Detect cycles in the dependency graph using depth-first search
	overallVisited := make(map[string]bool)
	for name := range e.components {
		if !overallVisited[name] { // Only start DFS from unvisited nodes
			recStack := make(map[string]bool) // Reset recursion stack for each DFS run
			cycle := detectCycle(name, dependenciesMap, overallVisited, recStack)
			if cycle != "" {
				err := fmt.Errorf("dependency cycle detected: %s", cycle)
				log.Printf("ERROR: %v", err)
				return err
			}
		}
	}
	log.Println("No dependency cycles detected.")

	// --- Phase 2: Start Components Asynchronously ---
	log.Println("Executing Start phase...")
	var startMu sync.Mutex                   // Protects shared state: depCount, started, finishedCount
	started := make(map[string]struct{})     // Set of successfully started component names
	finishedCount := 0                       // Number of components whose Start goroutine has finished (success or fail)
	componentError := make(map[string]error) // Store error per component if start fails

	startGroup, startCtx := errgroup.WithContext(ctx) // Use context for cancellation

	// Channel to coordinate starting dependents
	readyToProcess := make(chan string, len(e.components))

	// Function to launch the start process for a component
	launchStart := func(nameToStart string) {
		log.Printf("[%s] Launching start process...", nameToStart)
		envToStart := e.components[nameToStart]

		startGroup.Go(func() error {
			logPrefix := fmt.Sprintf("[%s] ", nameToStart)
			startTaskTime := time.Now()
			log.Printf("%sStarting component...", logPrefix)

			// Start the component's async process
			startResultChan := envToStart.Start(startCtx, e) // Pass Envs

			var startErr error
			select {
			case err, ok := <-startResultChan:
				if !ok {
					if startCtx.Err() != nil {
						startErr = fmt.Errorf("context cancelled during start: %w", startCtx.Err())
					} else {
						log.Printf("%sStart channel closed without error (success).", logPrefix)
						startErr = nil // Success
					}
				} else if err != nil {
					startErr = fmt.Errorf("start function returned error: %w", err)
				} else {
					log.Printf("%sStart function returned success (nil error).", logPrefix)
					startErr = nil // Explicit success
				}
			case <-startCtx.Done():
				startErr = fmt.Errorf("context cancelled waiting for start: %w", startCtx.Err())
			}

			duration := time.Since(startTaskTime)

			startMu.Lock()
			finishedCount++ // Increment finished count regardless of success/failure
			currentFinishedCount := finishedCount
			startMu.Unlock() // Unlock before logging potentially large amounts

			if startErr != nil {
				log.Printf("%sERROR: Component failed to start in %s: %v", logPrefix, duration, startErr)
				startMu.Lock()
				componentError[nameToStart] = startErr // Record the error
				startMu.Unlock()
				// Don't trigger dependents, return the error to the errgroup
				return fmt.Errorf("start %s failed: %w", nameToStart, startErr)
			}

			// --- Success ---
			log.Printf("%sComponent started successfully in %s.", logPrefix, duration)
			startMu.Lock()
			started[nameToStart] = struct{}{}
			startMu.Unlock()

			// Set duration using BaseEnv's unexported method (requires embedding)
			envToStart.SetStartDuration(duration)

			// Notify that this component is done, so dependents can be checked
			log.Printf("%sSignaling readiness to process dependents...", logPrefix)
			select {
			case readyToProcess <- nameToStart:
				log.Printf("%sSignaled readiness successfully.", logPrefix)
			case <-startCtx.Done():
				log.Printf("%sContext cancelled before signaling readiness.", logPrefix)
			default:
				// Should not happen if channel is sized correctly
				log.Printf("%sWarning: readyToProcess channel full when signaling completion.", logPrefix)
			}
			log.Printf("%sComponent start goroutine finished. Total finished: %d/%d", logPrefix, currentFinishedCount, len(e.components))
			return nil // Success for this goroutine
		})
	}

	// Kick off initial starters
	if len(initialStarters) == 0 && len(e.components) > 0 {
		return fmt.Errorf("no initial components found (all have dependencies?) - check for cycles or configuration errors")
	}
	for _, name := range initialStarters {
		launchStart(name)
	}

	// Process completion signals and launch dependents until all are finished or an error occurs
	processingDone := make(chan struct{})
	go func() {
		logPrefixProc := "[Dependency Processor] "
		defer close(processingDone)
		processedCount := 0 // How many components we have processed the *completion* of
		totalComponents := len(e.components)
		log.Printf("%sStarting. Waiting for %d components to signal completion.", logPrefixProc, totalComponents)

		for processedCount < totalComponents {
			select {
			case justStartedName := <-readyToProcess:
				processedCount++
				log.Printf("%sComponent '%s' signaled completion (%d/%d processed). Checking dependents...", logPrefixProc, justStartedName, processedCount, totalComponents)

				// A component finished successfully, check its dependents
				startMu.Lock()
				dependents := depGraph[justStartedName]
				log.Printf("%s'%s' has %d dependents: %v", logPrefixProc, justStartedName, len(dependents), dependents)
				for _, depName := range dependents {
					depCount[depName]--
					log.Printf("%sDecremented dependency count for '%s', remaining: %d", logPrefixProc, depName, depCount[depName])
					if depCount[depName] == 0 {
						// Check if context is already cancelled before launching more
						if startCtx.Err() != nil {
							log.Printf("%sContext cancelled, not launching dependent '%s'", logPrefixProc, depName)
							continue
						}
						log.Printf("%sDependencies met for '%s', launching start.", logPrefixProc, depName)
						launchStart(depName) // This starts a new goroutine in the startGroup
					}
				}
				startMu.Unlock()
				log.Printf("%sFinished processing dependents for '%s'.", logPrefixProc, justStartedName)

			case <-startCtx.Done():
				log.Printf("%sStopping dependency processing due to context cancellation: %v", logPrefixProc, startCtx.Err())
				return // Exit processing loop if context is cancelled
			}
		}
		log.Printf("%sFinished processing all %d component completions.", logPrefixProc, totalComponents)
	}()

	// Wait for all start goroutines to complete or for the first error
	log.Println("Waiting for all component Start goroutines...")
	err := startGroup.Wait() // This blocks until all launched goroutines finish or one errors
	log.Println("All component Start goroutines finished or group errored.")

	// Ensure the processing goroutine has also finished before proceeding
	log.Println("Waiting for dependency processor goroutine...")
	<-processingDone
	log.Println("Dependency processor goroutine finished.")

	if err != nil {
		log.Printf("Environment setup failed during Start phase: %v", err)
		log.Println("Performing cleanup due to failed start...")
		e.cleanupStarted(started) // Stop only successfully started components
		return err
	}

	// Final check: Ensure all components were processed
	startMu.Lock()
	finalFinishedCount := finishedCount
	finalStartedCount := len(started)
	finalErrors := componentError
	startMu.Unlock()

	log.Printf("Final Check: Registered=%d, Finished Goroutines=%d, Started Successfully=%d", len(e.components), finalFinishedCount, finalStartedCount)

	if finalFinishedCount != len(e.components) || finalStartedCount != len(e.components) {
		// This might indicate a dependency cycle, a logic error, or premature context cancellation
		errMsg := fmt.Errorf("environment setup finished inconsistently: %d components registered, %d finished, %d started successfully. Errors: %v", len(e.components), finalFinishedCount, finalStartedCount, finalErrors)
		log.Printf("ERROR: %v", errMsg)
		log.Println("Performing cleanup...")
		e.cleanupStarted(started)
		return errMsg
	}

	log.Printf("Environment setup complete in %s. Started %d components.", time.Since(startTime), finalStartedCount)
	return nil
}

// StopAll stops all registered components, logging errors.
func (e *Envs) StopAll() {
	log.Println("Stopping all environment components...")
	var wg sync.WaitGroup
	// Stop in parallel for faster cleanup
	for name, env := range e.components {
		wg.Add(1)
		go func(n string, en Environment) {
			logPrefix := fmt.Sprintf("[%s] ", n)
			defer wg.Done()
			log.Printf("%sStopping component...", logPrefix)
			stopStartTime := time.Now()
			if err := en.Stop(); err != nil {
				log.Printf("%sERROR stopping component: %v", logPrefix, err)
			} else {
				log.Printf("%sComponent stopped successfully in %s.", logPrefix, time.Since(stopStartTime))
			}
		}(name, env)
	}
	wg.Wait()
	log.Println("Finished stopping components.")
}

// cleanupStarted stops only the components that successfully started.
func (e *Envs) cleanupStarted(started map[string]struct{}) {
	log.Println("Cleaning up successfully started components...")
	var wg sync.WaitGroup
	for name := range started {
		env, ok := e.components[name]
		if !ok {
			log.Printf("Warning: Component '%s' marked as started but not found in registry during cleanup.", name)
			continue
		}
		wg.Add(1)
		go func(n string, en Environment) {
			logPrefix := fmt.Sprintf("[%s] ", n)
			defer wg.Done()
			log.Printf("%sStopping component during cleanup...", logPrefix)
			stopStartTime := time.Now()
			if err := en.Stop(); err != nil {
				log.Printf("%sERROR stopping component during cleanup: %v", logPrefix, err)
			} else {
				log.Printf("%sComponent stopped successfully during cleanup in %s.", logPrefix, time.Since(stopStartTime))
			}
		}(name, env)
	}
	wg.Wait()
	log.Println("Finished cleaning up started components.")
}

// detectCycle performs a depth-first search to find cycles in the dependency graph.
// Returns a string representation of the detected cycle, or empty string if no cycle found.
func detectCycle(node string, depMap map[string][]string, visited map[string]bool, recStack map[string]bool) string {
	// If node is already in recursion stack, cycle detected
	if recStack[node] {
		return node // Return the node where the cycle is detected
	}

	// If node has already been fully visited in this DFS run, no need to revisit
	if visited[node] {
		return ""
	}

	// Mark node as visited for this DFS run and add to recursion stack
	visited[node] = true
	recStack[node] = true

	// Visit all dependencies of this node
	for _, dep := range depMap[node] {
		if cycleStartNode := detectCycle(dep, depMap, visited, recStack); cycleStartNode != "" {
			// If a cycle is found downstream, prepend current node if it's part of the cycle path
			// The cycle path starts building up from the node that detected the back edge.
			if cycleStartNode == node {
				// We completed the cycle back to the start of this recursive call chain
				return node // Just return the start node name
			} else {
				// Part of an ongoing cycle string, prepend current node
				return fmt.Sprintf("%s -> %s", node, cycleStartNode)
			}
		}
	}

	// Remove node from recursion stack after visiting all its dependencies
	recStack[node] = false
	return "" // No cycle found starting from this node in this path
}

// GetComponent returns the registered component by name.
func (e *Envs) GetComponent(name string) (Environment, bool) {
	env, ok := e.components[name]
	return env, ok
}

// GetURL returns the URL of the component. Logs an error and returns "" if not found.
func (e *Envs) GetURL(name string) string {
	env, ok := e.components[name]
	if !ok {
		log.Printf("Error: Component '%s' not found when getting URL.", name)
		return ""
	}
	return env.URL()
}

// GetDetails returns the details of the component. Logs an error and returns nil if not found.
func (e *Envs) GetDetails(name string) interface{} {
	env, ok := e.components[name]
	if !ok {
		log.Printf("Error: Component '%s' not found when getting details.", name)
		return nil
	}
	return env.GetDetails()
}

// GetStartDuration returns the start duration of the component. Logs an error and returns 0 if not found.
func (e *Envs) GetStartDuration(name string) time.Duration {
	env, ok := e.components[name]
	if !ok {
		log.Printf("Error: Component '%s' not found when getting start duration.", name)
		return 0
	}
	return env.GetStartDuration()
}

// --- Global Instance and Proxy Functions ---

var defaultEnvs = NewEnvs()

// Register registers components with the default global environment manager.
func Register(envs ...Environment) {
	defaultEnvs.Register(envs...)
}

// Execute runs the setup process using the default global environment manager.
func Execute(ctx context.Context) error {
	return defaultEnvs.Execute(ctx)
}

// StopAll stops all components registered with the default global environment manager.
func StopAll() {
	defaultEnvs.StopAll()
}

// GetURL retrieves the URL from the default global environment manager.
func GetURL(name string) string {
	return defaultEnvs.GetURL(name)
}

// GetDetails retrieves details from the default global environment manager.
func GetDetails(name string) interface{} {
	return defaultEnvs.GetDetails(name)
}

// GetStartDuration retrieves the start duration from the default global environment manager.
func GetStartDuration(name string) time.Duration {
	return defaultEnvs.GetStartDuration(name)
}

// GetComponent returns the registered component by name.
func GetComponent(name string) (Environment, bool) {
	return defaultEnvs.GetComponent(name)
}
