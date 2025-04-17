package env

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	_ "github.com/lib/pq" // Import postgres driver for verification ping
)

const PrismaComponentName = "prisma-migrate"
const TestConfigWorkspaceFolder = ".." // Relative path from tests dir to project root

// PrismaEnv handles running Prisma database migrations and seeding.
type PrismaEnv struct {
	BaseEnv // Embed BaseEnv for duration and default methods
	// --- Component-specific state ---
	// PrismaEnv doesn't typically store state beyond what BaseEnv provides.
}

// NewPrismaEnv creates a new Prisma migration component.
func NewPrismaEnv() *PrismaEnv {
	return &PrismaEnv{
		BaseEnv: BaseEnv{name: PrismaComponentName},
	}
}

// Configure declares the dependency on the database component.
func (e *PrismaEnv) Configure(envs *Envs) (dependencies []string, err error) {
	// Depends only on the database being configured (so its intended URL is available)
	return []string{DBComponentName}, nil
}

// Start runs the npx prisma commands.
func (e *PrismaEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		// Get Database URL from the dependency component.
		// At this stage, the DB component has finished Configure, so its URL (DSN) should be available.
		// The DB container might still be starting up, but Prisma CLI needs the DSN now.
		databaseURL := envs.GetURL(DBComponentName)
		if databaseURL == "" {
			resultChan <- fmt.Errorf("%s dependency '%s' URL is not available", e.Name(), DBComponentName)
			return
		}

		// Wait briefly for the database to likely accept connections
		if err := e.waitForDB(ctx, databaseURL); err != nil {
			resultChan <- fmt.Errorf("database not ready for migrations: %w", err)
			return
		}

		prismaDir := filepath.Join(TestConfigWorkspaceFolder, "portal")
		envVars := append(os.Environ(), "GATE4AI_DATABASE_URL="+databaseURL)

		// --- Run Prisma Commands ---
		commands := []struct {
			name string
			args []string
			warn bool // If true, log error but don't fail the component
		}{
			{"generate", []string{"npx", "prisma", "generate"}, false},
			{"db push", []string{"npx", "prisma", "db", "push", "--force-reset", "--accept-data-loss"}, false},
			// {"db seed", []string{"npx", "prisma", "db", "seed"}, true}, // Example: Make seed failures non-fatal
			{"db seed", []string{"npx", "prisma", "db", "seed"}, false}, // Make seed failures fatal based on original logic
		}

		for _, cmdInfo := range commands {
			log.Printf("Running prisma %s...", cmdInfo.name)
			cmd := exec.CommandContext(ctx, cmdInfo.args[0], cmdInfo.args[1:]...)
			cmd.Dir = prismaDir
			cmd.Env = envVars
			// Pipe output for visibility - consider using buffers if needed
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			err := cmd.Run()

			// Check context cancellation first
			if ctx.Err() != nil {
				log.Printf("Context cancelled during prisma %s", cmdInfo.name)
				resultChan <- ctx.Err()
				return
			}

			if err != nil {
				errMsg := fmt.Errorf("failed to run prisma %s: %w", cmdInfo.name, err)
				if cmdInfo.warn {
					log.Printf("Warning: %v", errMsg) // Log as warning, continue
				} else {
					log.Printf("Error: %v", errMsg)
					resultChan <- errMsg // Fail component
					return
				}
			}
		}

		log.Printf("Component %s finished successfully.", e.Name())
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// waitForDB pings the database until it's available or context is cancelled.
func (e *PrismaEnv) waitForDB(ctx context.Context, databaseURL string) error {
	log.Printf("Waiting for database (%s) to become available for migrations...", DBComponentName)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Increased timeout for DB readiness check
	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timed out waiting for database: %w", timeoutCtx.Err())
		case <-ticker.C:
			db, err := sql.Open("postgres", databaseURL)
			if err != nil {
				// Should not happen with valid DSN unless driver is missing
				log.Printf("Error opening database connection (will retry): %v", err)
				continue
			}
			pingCtx, pingCancel := context.WithTimeout(timeoutCtx, 1*time.Second)
			err = db.PingContext(pingCtx)
			pingCancel()
			db.Close() // Close connection immediately after ping

			if err == nil {
				log.Printf("Database is available.")
				// Add a small grace period?
				time.Sleep(500 * time.Millisecond)
				return nil // Success
			}
			// Log ping error but continue waiting
			// log.Printf("Database ping failed (will retry): %v", err)
		}
	}
}

// Stop for PrismaEnv is a no-op as it's a one-time task.
// func (e *PrismaEnv) Stop() error { return nil } // Uses BaseEnv default

// URL is not applicable for Prisma migrations.
// func (e *PrismaEnv) URL() string { return "" } // Uses BaseEnv default

// GetDetails is not applicable for Prisma migrations.
// func (e *PrismaEnv) GetDetails() interface{} { return nil } // Uses BaseEnv default
