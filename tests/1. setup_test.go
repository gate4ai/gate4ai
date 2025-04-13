package tests

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gate4ai/mcp/gateway"
	"github.com/gate4ai/mcp/server"
	"github.com/gate4ai/mcp/shared/config"
	"github.com/playwright-community/playwright-go"

	_ "github.com/lib/pq"
	"go.uber.org/zap"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/shirou/gopsutil/v3/process"
)

const TEST_CONFIG_WORKSPACE_FOLDER = ".."

var (
	// Global variables for use across tests
	DATABASE_URL       string
	PORTAL_URL         string //in migration set PORTAL_URL = GATEWAY_URL // all test doing over reverse proxy in gateway
	GATEWAY_URL        string
	EXAMPLE_SERVER_URL string
	MAILHOG_API_URL    string
	EMAIL_SMTP_SERVER  SmtpServerType
	pw                 *playwright.Playwright
)

type SmtpServerType struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Secure bool   `json:"secure"`
	Auth   struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	} `json:"auth"`
}

func TestMain(m *testing.M) {
	exitCode := 1
	defer func() {
		os.Exit(exitCode)
	}()

	// Use a context that can be canceled on cleanup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a channel to collect results from initial containers
	type initResult struct {
		name   string
		result interface{}
		err    error
	}
	containerChan := make(chan initResult, 3) // Buffer for the 3 initial containers (PostgreSQL, MailHog, Playwright)
	resultChan := make(chan initResult, 3)    // Buffer for other tasks (portal, gateway, example)

	// Step 1: Start the PostgreSQL and MailHog containers in parallel
	go func() {
		dbContainer, err := startDB(ctx)
		if err != nil {
			containerChan <- initResult{"database", nil, fmt.Errorf("failed to start database container: %v", err)}
			return
		}
		containerChan <- initResult{"database", dbContainer, nil}
	}()

	go func() {
		mailhogContainer, err := startMailHog(ctx)
		if err != nil {
			containerChan <- initResult{"mailhog", nil, fmt.Errorf("failed to start mailhog container: %v", err)}
			return
		}
		containerChan <- initResult{"mailhog", mailhogContainer, nil}
	}()

	// Initialize Playwright in parallel
	go func() {
		playwright, err := playwright.Run()
		if err != nil {
			containerChan <- initResult{"playwright", nil, fmt.Errorf("failed to start playwright: %v", err)}
			return
		}
		containerChan <- initResult{"playwright", playwright, nil}
	}()

	// Wait for both containers to start
	var dbContainer testcontainers.Container
	var mailhogContainer testcontainers.Container
	var errors []error

	for i := 0; i < 3; i++ {
		result := <-containerChan
		if result.err != nil {
			errors = append(errors, result.err)
			continue
		}

		switch result.name {
		case "database":
			dbContainer = result.result.(testcontainers.Container)
		case "mailhog":
			mailhogContainer = result.result.(testcontainers.Container)
		case "playwright":
			pw = result.result.(*playwright.Playwright)
		}
	}

	// Check if there were errors starting containers
	if len(errors) > 0 {
		for _, err := range errors {
			log.Printf("Container initialization error: %v\n", err)
		}
		return
	}

	// Step 2: Determine all URLs
	portalPort, gatewayPort, examplePort, err := FindAvailablePort3()
	if err != nil {
		log.Printf("Failed to find available port for portal: %v\n", err)
		cleanup(ctx, dbContainer, mailhogContainer, nil, nil, nil)
		return
	}
	PORTAL_URL = fmt.Sprintf("http://localhost:%d", portalPort)
	os.Setenv("GATE4AI_PORTAL_URL", PORTAL_URL)

	GATEWAY_URL = fmt.Sprintf("http://localhost:%d", gatewayPort)
	os.Setenv("GATE4AI_GATEWAY_URL", GATEWAY_URL)

	EXAMPLE_SERVER_URL = fmt.Sprintf("http://localhost:%d/sse?key=test-key-user1", examplePort)
	os.Setenv("GATE4AI_EXAMPLE_SERVER_URL", EXAMPLE_SERVER_URL)

	// Step 3: Run Prisma migrations
	if err := runPrismaMigrations(); err != nil {
		log.Printf("Failed to run prisma migrations: %v\n", err)
		cleanup(ctx, dbContainer, mailhogContainer, nil, nil, nil)
		return
	}

	// Step 4: Update database config with URLs
	if err := updateDatabaseSettings(); err != nil {
		log.Printf("Failed to update database settings: %v\n", err)
		cleanup(ctx, dbContainer, mailhogContainer, nil, nil, nil)
		return
	}

	// Step 6: Start the Nuxt portal server in a goroutine
	go func() {
		portalServer, err := startPortalServer(ctx, portalPort)
		if err != nil {
			resultChan <- initResult{"portal", nil, fmt.Errorf("failed to start portal server: %v", err)}
			return
		}
		resultChan <- initResult{"portal", portalServer, nil}
	}()

	// Step 7: Start the gateway server in a goroutine
	go func() {
		gatewayServer, err := startGatewayServer(ctx, gatewayPort)
		if err != nil {
			resultChan <- initResult{"gateway", nil, fmt.Errorf("failed to start gateway server: %v", err)}
			return
		}
		resultChan <- initResult{"gateway", gatewayServer, nil}
	}()

	// Step 8: Start the example server in a goroutine
	go func() {
		exampleServer, err := startExampleServer(ctx, examplePort)
		if err != nil {
			resultChan <- initResult{"example", nil, fmt.Errorf("failed to start example server: %v", err)}
			return
		}
		resultChan <- initResult{"example", exampleServer, nil}
	}()

	// Collect results and check for errors
	var portalServer *Server
	var gatewayServer *Server
	var exampleServer *Server

	// Wait for all init tasks to complete
	for i := 0; i < 3; i++ { // Only wait for 3 goroutines now (portal, gateway, example)
		result := <-resultChan
		if result.err != nil {
			errors = append(errors, fmt.Errorf("%s: %v", result.name, result.err))
			continue
		}

		switch result.name {
		case "portal":
			portalServer = result.result.(*Server)
		case "gateway":
			gatewayServer = result.result.(*Server)
		case "example":
			exampleServer = result.result.(*Server)
		}
	}

	// Check if any errors occurred
	if len(errors) > 0 {
		for _, err := range errors {
			log.Printf("Initialization error: %v\n", err)
		}
		cleanup(ctx, dbContainer, mailhogContainer, portalServer, gatewayServer, exampleServer)
		return
	}

	// Clean up resources on exit
	defer cleanup(ctx, dbContainer, mailhogContainer, portalServer, gatewayServer, exampleServer)

	// Run the tests and get the exit code
	exitCode = m.Run()
}

// cleanup handles graceful termination of all resources
func cleanup(ctx context.Context, dbContainer, mailhogContainer testcontainers.Container,
	portalServer, gatewayServer, exampleServer *Server) {
	if dbContainer != nil {
		log.Println("Stopping PostgreSQL container...")
		if err := dbContainer.Terminate(ctx); err != nil {
			log.Printf("Failed to stop PostgreSQL container: %v\n", err)
		}
	}
	if mailhogContainer != nil {
		log.Println("Stopping MailHog container...")
		if err := mailhogContainer.Terminate(ctx); err != nil {
			log.Printf("Failed to stop MailHog container: %v\n", err)
		}
	}
	if portalServer != nil {
		log.Println("Stopping portal server...")
		portalServer.Stop()
	}
	if gatewayServer != nil {
		log.Println("Stopping gateway server...")
		gatewayServer.Stop()
	}
	if exampleServer != nil {
		log.Println("Stopping example server...")
		exampleServer.Stop()
	}
	if pw != nil {
		log.Println("Stopping Playwright...")
		pw.Stop()
	}
}

// startDB starts a PostgreSQL 17 Alpine container
func startDB(ctx context.Context) (testcontainers.Container, error) {
	// PostgreSQL container configuration
	req := testcontainers.ContainerRequest{
		Image:        "postgres:17-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "postgres",
			"POSTGRES_PASSWORD": "postgres",
			"POSTGRES_DB":       "gate4ai",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp"),
	}

	// Start the container
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	// Get the container's mapped port
	mappedPort, err := container.MappedPort(ctx, "5432")
	if err != nil {
		return nil, fmt.Errorf("failed to get mapped port: %w", err)
	}

	// Get the container's host
	host, err := container.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	// Build the DSN
	dsn := fmt.Sprintf("postgresql://postgres:postgres@%s:%s/gate4ai?sslmode=disable", host, mappedPort.Port())

	// Set the global DATABASE_URL for other tests to use
	DATABASE_URL = dsn
	os.Setenv("GATE4AI_DATABASE_URL", dsn)

	log.Printf("PostgreSQL container started, DSN: %s\n", dsn)
	return container, nil
}

// startMailHog starts a MailHog container for SMTP testing
func startMailHog(ctx context.Context) (testcontainers.Container, error) {
	// MailHog container configuration
	req := testcontainers.ContainerRequest{
		Image:        "mailhog/mailhog:latest",
		ExposedPorts: []string{"1025/tcp", "8025/tcp"},
		WaitingFor:   wait.ForHTTP("/").WithPort("8025/tcp"),
	}

	// Start the container
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start MailHog container: %w", err)
	}

	// Get the container's SMTP port
	smtpPort, err := container.MappedPort(ctx, "1025")
	if err != nil {
		return nil, fmt.Errorf("failed to get SMTP port: %w", err)
	}

	// Get the container's UI port
	uiPort, err := container.MappedPort(ctx, "8025")
	if err != nil {
		return nil, fmt.Errorf("failed to get UI port: %w", err)
	}

	// Get the container's host
	host, err := container.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	// Set the global EMAIL_SMTP_SERVER for other tests to use
	MAILHOG_API_URL = fmt.Sprintf("http://%s:%s", host, uiPort.Port())

	// Save SMTP server details to be updated in the database
	EMAIL_SMTP_SERVER = SmtpServerType{
		Host:   host,
		Port:   smtpPort.Int(),
		Secure: false,
		Auth: struct {
			User string `json:"user"`
			Pass string `json:"pass"`
		}{
			User: "",
			Pass: "",
		},
	}

	log.Printf("MailHog container started, SMTP: %s:%s, Web UI: %s:%s\n",
		host, smtpPort.Port(), host, uiPort.Port())

	return container, nil
}

// runPrismaMigrations runs all the necessary Prisma commands to set up the database
func runPrismaMigrations() error {
	// Directory containing the Prisma files
	prismaDir := filepath.Join(TEST_CONFIG_WORKSPACE_FOLDER + "/portal")

	// Create a command to generate Prisma client
	generateCmd := exec.Command("npx", "prisma", "generate")
	generateCmd.Dir = prismaDir
	generateCmd.Stdout = os.Stdout
	generateCmd.Stderr = os.Stderr
	generateCmd.Env = append(os.Environ(), "GATE4AI_DATABASE_URL="+DATABASE_URL)

	log.Println("Generating Prisma client...")
	if err := generateCmd.Run(); err != nil {
		return fmt.Errorf("failed to generate prisma client: %w", err)
	}

	// Use db push to create the schema directly from the Prisma schema
	// This creates tables without requiring migrations
	pushCmd := exec.Command("npx", "prisma", "db", "push", "--force-reset", "--accept-data-loss")
	pushCmd.Dir = prismaDir
	pushCmd.Stdout = os.Stdout
	pushCmd.Stderr = os.Stderr
	pushCmd.Env = append(os.Environ(), "GATE4AI_DATABASE_URL="+DATABASE_URL)

	log.Println("Creating database schema...")
	if err := pushCmd.Run(); err != nil {
		return fmt.Errorf("failed to create database schema: %w", err)
	}

	// Verify that the tables exist before seeding
	if err := verifyTablesExist(); err != nil {
		return fmt.Errorf("database tables not ready: %w", err)
	}

	// Now run the seed script separately
	seedCmd := exec.Command("npx", "prisma", "db", "seed")
	seedCmd.Dir = prismaDir
	seedCmd.Stdout = os.Stdout
	seedCmd.Stderr = os.Stderr
	seedCmd.Env = append(os.Environ(), "GATE4AI_DATABASE_URL="+DATABASE_URL)

	log.Println("Seeding database...")
	if err := seedCmd.Run(); err != nil {
		// If seeding fails, don't fail the tests, just log the error
		log.Printf("Warning: Database seeding encountered an error: %v\n", err)
		log.Println("Tests will continue, but may have incomplete test data")
	}

	return nil
}

// verifyTablesExist checks if necessary tables exist in the database
func verifyTablesExist() error {
	// Connect to the database
	db, err := sql.Open("postgres", DATABASE_URL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Set a timeout for queries
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// List of required tables to check (add more as needed)
	requiredTables := []string{"User", "Settings"}

	for _, table := range requiredTables {
		var exists bool
		query := `
			SELECT EXISTS (
				SELECT FROM information_schema.tables 
				WHERE table_schema = 'public' 
				AND table_name = $1
			)
		`
		err := db.QueryRowContext(ctx, query, table).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to check if table %s exists: %w", table, err)
		}

		if !exists {
			// Wait briefly and check again to allow for any async operations
			time.Sleep(2 * time.Second)
			err := db.QueryRowContext(ctx, query, table).Scan(&exists)
			if err != nil {
				return fmt.Errorf("failed to recheck if table %s exists: %w", table, err)
			}

			if !exists {
				return fmt.Errorf("required table %s does not exist", table)
			}
		}
	}

	return nil
}

// updateDatabaseSettings updates the settings in the database with our URLs
func updateDatabaseSettings() error {
	// Update email SMTP server settings
	if err := updateSetting("email_smtp_server", EMAIL_SMTP_SERVER); err != nil {
		return fmt.Errorf("failed to update email_smtp_server: %w", err)
	}

	// Update gateway_listen_address with our port
	_, portStr, _ := net.SplitHostPort(GATEWAY_URL[7:]) // Remove http:// and split host:port
	gatewayAddress := fmt.Sprintf(":%s", portStr)
	if err := updateSetting("gateway_listen_address", gatewayAddress); err != nil {
		return fmt.Errorf("failed to update gateway_listen_address: %w", err)
	}

	// Update frontend proxy address
	if err := updateSetting("url_how_gateway_proxy_connect_to_the_portal", PORTAL_URL); err != nil {
		return fmt.Errorf("failed to update url_how_gateway_proxy_connect_to_the_portal: %w", err)
	}

	PORTAL_URL = GATEWAY_URL // all test doing over reverse proxy in gateway

	if err := updateSetting("url_how_users_connect_to_the_portal", PORTAL_URL); err != nil {
		return fmt.Errorf("failed to update url_how_gateway_proxy_connect_to_the_portal: %w", err)
	}

	// Update general gateway address
	if err := updateSetting("general_gateway_address", GATEWAY_URL); err != nil {
		return fmt.Errorf("failed to update general_gateway_address: %w", err)
	}

	log.Println("Database settings updated with URLs")
	return nil
}

// Server represents the portal server instance
type Server struct {
	cmd  *exec.Cmd
	port int
	url  string
	ctx  context.CancelFunc
}

// startPortalServer starts the Nuxt portal server
func startPortalServer(ctx context.Context, port int) (*Server, error) {
	serverCtx, cancel := context.WithCancel(ctx)

	// Set up environment variables for the server
	env := append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		fmt.Sprintf("GATE4AI_DATABASE_URL=%s", DATABASE_URL),
		fmt.Sprintf("JWT_SECRET=%s", "a-secure-test-secret-key-for-go-tests-needs-to-be-at-least-32-chars"),
		"NODE_ENV=production",
	)

	portalDir := filepath.Join(TEST_CONFIG_WORKSPACE_FOLDER + "/portal")

	// First build the application
	buildCmd := exec.CommandContext(serverCtx, "npm", "run", "build")
	buildCmd.Dir = portalDir
	buildCmd.Env = env
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	log.Println("Building Nuxt portal in production mode...")
	if err := buildCmd.Run(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to build portal: %w", err)
	}

	// Start the Nuxt server in production mode using preview
	cmd := exec.CommandContext(serverCtx, "npm", "run", "preview", "--", "--port", fmt.Sprintf("%d", port))
	cmd.Dir = portalDir // Path to portal directory
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start portal server: %w", err)
	}

	// Create the server instance
	server := &Server{
		cmd:  cmd,
		port: port,
		url:  PORTAL_URL,
		ctx:  cancel,
	}

	// Wait for the server to be ready
	if err := waitForServer(PORTAL_URL + "/api/status"); err != nil {
		server.Stop()
		return nil, err
	}

	log.Printf("Portal server started in production mode on %s\n", PORTAL_URL)
	return server, nil
}

// waitForServer polls the server until it's responsive
func waitForServer(url string) error {
	// Poll with timeout
	timeout := time.After(120 * time.Second)
	tick := time.Tick(1 * time.Second)

	log.Printf("Waiting for server to become available at %s...\n", url)

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timed out waiting for server to start at %s", url)
		case <-tick:
			resp, err := http.Get(url)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					// Server is ready
					time.Sleep(1 * time.Second)
					return nil
				}
			}
		}
	}
}

// Stop stops the portal server
func (s *Server) Stop() {
	// Cancel the context to stop the command
	s.ctx()

	// Get process group ID
	pgid, err := syscall.Getpgid(s.cmd.Process.Pid)
	if err == nil {
		// Kill the entire process group to ensure all child processes are terminated
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			log.Printf("Failed to terminate process group: %v\n", err)

			// Try harder if needed
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
				log.Printf("Failed to kill process group: %v\n", err)
			}
		}
	} else {
		log.Printf("Failed to get process group ID: %v\n", err)
		// Fallback to direct kill
		if err := s.cmd.Process.Kill(); err != nil {
			log.Printf("Failed to kill server process: %v\n", err)
		}
	}

	// Wait for the process to exit
	if err := s.cmd.Wait(); err != nil {
		log.Printf("Server process exited with error: %v\n", err)
	}
}

// updateSetting updates a setting in the database
func updateSetting(key string, value interface{}) error {
	// Connect to the database
	db, err := sql.Open("postgres", DATABASE_URL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Marshal the value to JSON
	var valueJSON []byte
	switch v := value.(type) {
	case string:
		valueJSON = []byte(fmt.Sprintf("%q", v))
	case json.RawMessage:
		valueJSON = v
	default:
		valueJSON, err = json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value to JSON: %w", err)
		}
	}

	// Check if the setting exists
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM "Settings" WHERE key = $1`, key).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check if setting exists: %w", err)
	}

	if count > 0 {
		// Update existing setting
		_, err = db.Exec(`UPDATE "Settings" SET value = $1 WHERE key = $2`, valueJSON, key)
		if err != nil {
			return fmt.Errorf("failed to update setting: %w", err)
		}
	} else {
		// Insert new setting with minimal required fields
		_, err = db.Exec(`INSERT INTO "Settings" (key, group, name, description, value) VALUES ($1, 'test', $1, $1, $2)`,
			key, valueJSON)
		if err != nil {
			return fmt.Errorf("failed to insert setting: %w", err)
		}
	}

	return nil
}

// startGatewayServer starts the Gateway MCP service
func startGatewayServer(ctx context.Context, port int) (*Server, error) {
	serverCtx, cancel := context.WithCancel(ctx)

	//запустить GW
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	cfgGw, err := config.NewDatabaseConfig(DATABASE_URL, logger)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}
	_, err = gateway.Start(serverCtx, logger.With(zap.String("s", "gateway")), cfgGw, fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("Failed to find available port: %v", err)
	}

	// Create the server instance
	server := &Server{
		port: port,
		url:  GATEWAY_URL,
		ctx:  cancel,
	}

	// Wait for the server to be ready
	if err := waitForServer(GATEWAY_URL + "/status"); err != nil {
		server.Stop()
		return nil, err
	}

	log.Printf("Gateway server started on %s\n", GATEWAY_URL)
	return server, nil
}

// startExampleServer starts the example MCP server
func startExampleServer(ctx context.Context, port int) (*Server, error) {
	serverCtx, cancel := context.WithCancel(ctx)

	//запустить GW
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	cfg, err := config.NewYamlConfig(TEST_CONFIG_WORKSPACE_FOLDER+"/server/cmd/mcp-example-server/config.yaml", logger)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}
	err = server.StartExample(serverCtx, logger.With(zap.String("s", "gateway")), cfg, fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("Failed to find available port: %v", err)
	}

	// Create the server instance
	server := &Server{
		port: port,
		url:  fmt.Sprintf("http://localhost:%d", port),
		ctx:  cancel,
	}

	// Wait for the server to be ready
	if err := waitForServer(fmt.Sprintf("http://localhost:%d/status", port)); err != nil {
		server.Stop()
		return nil, err
	}

	log.Printf("Example server started on %s\n", EXAMPLE_SERVER_URL)
	return server, nil
}

func isDebugMode() bool {
	pid := int32(os.Getppid())
	parentProc, err := process.NewProcess(pid)
	if err != nil {
		log.Printf("Error getting parent process: %v", err)
		return false
	}

	parentName, err := parentProc.Name()
	if err != nil {
		log.Printf("Error getting parent process name: %v", err)
		return false
	}

	return parentName == "dlv"
}
