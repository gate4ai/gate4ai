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
		configureGroup.Go(func() error {
			select {
			case <-configureCtx.Done(): // Check if another configure task failed
				return configureCtx.Err()
			default:
				log.Printf("Configuring component: %s", currentEnv.Name())
				deps, err := currentEnv.Configure(e) // Pass Envs for potential port allocation
				if err != nil {
					log.Printf("ERROR: Failed to configure component %s: %v", currentEnv.Name(), err)
					return fmt.Errorf("configure %s failed: %w", currentEnv.Name(), err)
				}
				// Safely store dependencies (though this part runs concurrently, map access needs care if Execute ran concurrently itself, which it doesn't)
				// For simplicity, we collect results after the group wait. A mutex could be used if needed.
				dependenciesMap[currentEnv.Name()] = deps // Store dependencies returned by Configure
				log.Printf("Component %s configured, dependencies: %v", currentEnv.Name(), deps)
				return nil
			}
		})
	}

	if err := configureGroup.Wait(); err != nil {
		log.Printf("Environment setup failed during Configure phase: %v", err)
		// No components started yet, no cleanup needed.
		return err // Return the first configuration error
	}
	log.Println("Configure phase completed.")

	// --- Build Graph & Check Dependencies ---
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

	// Detect cycles in the dependency graph using depth-first search
	for name := range e.components {
		visited := make(map[string]bool)  // Track visited nodes in current traversal
		recStack := make(map[string]bool) // Track nodes in current recursion stack
		cycle := detectCycle(name, dependenciesMap, visited, recStack)
		if cycle != "" {
			err := fmt.Errorf("dependency cycle detected: %s", cycle)
			log.Printf("ERROR: %v", err)
			return err
		}
	}

	// --- Phase 2: Start Components Asynchronously ---
	log.Println("Executing Start phase...")
	var startMu sync.Mutex               // Protects shared state: depCount, started, finishedCount
	started := make(map[string]struct{}) // Set of successfully started component names
	finishedCount := 0

	startGroup, startCtx := errgroup.WithContext(ctx) // Use context for cancellation

	// Channel to coordinate starting dependents
	// Using a buffered channel to avoid blocking goroutines if the main loop is busy
	readyToProcess := make(chan string, len(e.components))

	// Function to launch the start process for a component
	launchStart := func(nameToStart string) {
		envToStart := e.components[nameToStart]

		startGroup.Go(func() error {
			startTaskTime := time.Now()
			log.Printf("Starting component: %s", nameToStart)

			// Start the component's async process
			startResultChan := envToStart.Start(startCtx, e) // Pass Envs

			var startErr error
			select {
			case err, ok := <-startResultChan:
				if !ok {
					// Channel closed without sending a value - treat as success unless context cancelled
					if startCtx.Err() != nil {
						startErr = fmt.Errorf("context cancelled during start of %s: %w", nameToStart, startCtx.Err())
					} else {
						startErr = nil // Success
					}
				} else {
					startErr = err // Value received (nil for success, error otherwise)
				}
			case <-startCtx.Done():
				startErr = fmt.Errorf("context cancelled waiting for start of %s: %w", nameToStart, startCtx.Err())
			}

			duration := time.Since(startTaskTime)

			startMu.Lock()
			defer startMu.Unlock()

			finishedCount++ // Increment finished count regardless of success/failure

			if startErr != nil {
				log.Printf("ERROR: Component '%s' failed to start in %s: %v", nameToStart, duration, startErr)
				// Don't trigger dependents, return the error to the errgroup
				return fmt.Errorf("start %s failed: %w", nameToStart, startErr)
			}

			// --- Success ---
			log.Printf("Component '%s' started successfully in %s.", nameToStart, duration)
			started[nameToStart] = struct{}{}

			// Set duration using BaseEnv's unexported method (requires embedding)
			envToStart.SetStartDuration(duration)

			// Notify that this component is done, so dependents can be checked
			// Use non-blocking send in case the channel buffer is full (shouldn't happen with correct sizing)
			select {
			case readyToProcess <- nameToStart:
			default:
				// Should not happen if channel is sized correctly
				log.Printf("Warning: readyToProcess channel full when signaling completion of %s", nameToStart)
			}

			return nil // Success for this goroutine
		})
	}

	// Kick off initial starters
	for _, name := range initialStarters {
		launchStart(name)
	}

	// Process completion signals and launch dependents until all are finished or an error occurs
	processingDone := make(chan struct{})
	go func() {
		defer close(processingDone)
		processedCount := 0 // How many components we have processed the *completion* of
		for processedCount < len(e.components) {
			select {
			case justStartedName := <-readyToProcess:
				processedCount++
				// A component finished successfully, check its dependents
				startMu.Lock()
				dependents := depGraph[justStartedName]
				log.Printf("Component '%s' finished, checking %d dependents: %v", justStartedName, len(dependents), dependents)
				for _, depName := range dependents {
					depCount[depName]--
					log.Printf("Decremented dependency count for '%s', remaining: %d", depName, depCount[depName])
					if depCount[depName] == 0 {
						// Check if context is already cancelled before launching more
						if startCtx.Err() != nil {
							log.Printf("Context cancelled, not launching dependent '%s'", depName)
							continue
						}
						log.Printf("Component '%s' dependencies met, launching start.", depName)
						launchStart(depName)
					}
				}
				startMu.Unlock()
			case <-startCtx.Done():
				log.Printf("Stopping dependency processing due to context cancellation.")
				return // Exit processing loop if context is cancelled
			}
		}
	}()

	// Wait for all start goroutines to complete or for the first error
	err := startGroup.Wait()

	// Ensure the processing goroutine has finished before proceeding
	<-processingDone

	if err != nil {
		log.Printf("Environment setup failed during Start phase: %v", err)
		log.Println("Performing cleanup due to failed start...")
		e.cleanupStarted(started) // Stop only successfully started components
		return err
	}

	// Final check: Ensure all components were processed
	startMu.Lock()
	allFinished := finishedCount == len(e.components)
	allStartedSuccessfully := len(started) == len(e.components)
	startMu.Unlock()

	if !allFinished || !allStartedSuccessfully {
		// This might indicate a dependency cycle, a logic error, or premature context cancellation
		err := fmt.Errorf("environment setup finished inconsistently: %d components registered, %d finished, %d started successfully", len(e.components), finishedCount, len(started))
		log.Printf("ERROR: %v", err)
		log.Println("Performing cleanup...")
		e.cleanupStarted(started)
		return err
	}

	log.Printf("Environment setup complete in %s. Started %d components.", time.Since(startTime), len(started))
	return nil
}

// StopAll stops all registered components, logging errors.
func (e *Envs) StopAll() {
	log.Println("Stopping all environment components...")
	var wg sync.WaitGroup
	// Stop in reverse order? Not strictly necessary, parallel stop is usually fine.
	for name, env := range e.components {
		wg.Add(1)
		go func(n string, en Environment) {
			defer wg.Done()
			log.Printf("Stopping component: %s", n)
			if err := en.Stop(); err != nil {
				log.Printf("Error stopping component %s: %v", n, err)
			} else {
				log.Printf("Component %s stopped.", n)
			}
		}(name, env)
	}
	wg.Wait()
	log.Println("Finished stopping components.")
}

// cleanupStarted stops only the components that successfully started.
func (e *Envs) cleanupStarted(started map[string]struct{}) {
	var wg sync.WaitGroup
	for name := range started {
		env, ok := e.components[name]
		if !ok {
			continue // Should not happen
		}
		wg.Add(1)
		go func(n string, en Environment) {
			defer wg.Done()
			log.Printf("Stopping successfully started component: %s", n)
			if err := en.Stop(); err != nil {
				log.Printf("Error stopping component %s during cleanup: %v", n, err)
			} else {
				log.Printf("Component %s stopped during cleanup.", n)
			}
		}(name, env)
	}
	wg.Wait()
	log.Println("Finished cleaning up started components.")
}

// detectCycle performs a depth-first search to find cycles in the dependency graph.
// Returns a string representation of the detected cycle, or empty string if no cycle found.
func detectCycle(node string, depMap map[string][]string, visited map[string]bool, recStack map[string]bool) string {
	if !visited[node] {
		visited[node] = true
		recStack[node] = true

		// Visit all dependencies of this node
		for _, dep := range depMap[node] {
			// If dependency is in the recursion stack, we found a cycle
			if recStack[dep] {
				return fmt.Sprintf("%s -> %s", node, dep)
			}

			// If not visited, do DFS and check for cycle
			if !visited[dep] {
				if cycle := detectCycle(dep, depMap, visited, recStack); cycle != "" {
					// Prepend current node to the cycle path
					return fmt.Sprintf("%s -> %s", node, cycle)
				}
			}
		}
	}

	// Remove node from recursion stack after traversal
	recStack[node] = false
	return ""
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
