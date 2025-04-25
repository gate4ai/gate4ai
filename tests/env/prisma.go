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
		logPrefix := fmt.Sprintf("[%s] ", e.Name())
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

		// Get Database URL from the dependency component.
		log.Printf("%sFetching database URL from component '%s'", logPrefix, DBComponentName)
		databaseURL := envs.GetURL(DBComponentName)
		if databaseURL == "" {
			err := fmt.Errorf("%sdependency '%s' URL is not available", logPrefix, DBComponentName)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sDatabase URL obtained: %s", logPrefix, databaseURL)

		// Wait for the database to likely accept connections
		log.Printf("%sWaiting for database readiness...", logPrefix)
		if err := e.waitForDB(ctx, databaseURL); err != nil {
			err = fmt.Errorf("database not ready for migrations: %w", err)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sDatabase is ready.", logPrefix)

		prismaDir := filepath.Join(TestConfigWorkspaceFolder, "portal")
		envVars := append(os.Environ(), "GATE4AI_DATABASE_URL="+databaseURL)
		log.Printf("%sUsing Prisma directory: %s", logPrefix, prismaDir)

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
			log.Printf("%sRunning prisma %s...", logPrefix, cmdInfo.name)
			cmd := exec.CommandContext(ctx, cmdInfo.args[0], cmdInfo.args[1:]...)
			cmd.Dir = prismaDir
			cmd.Env = envVars
			// Pipe output for visibility - consider using buffers if needed
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			cmdStartTime := time.Now()
			err := cmd.Run()
			cmdDuration := time.Since(cmdStartTime)

			// Check context cancellation first
			if ctx.Err() != nil {
				log.Printf("%sContext cancelled during prisma %s", logPrefix, cmdInfo.name)
				resultChan <- ctx.Err()
				return
			}

			if err != nil {
				errMsg := fmt.Errorf("failed to run prisma %s (duration: %s): %w", cmdInfo.name, cmdDuration, err)
				if cmdInfo.warn {
					log.Printf("%sWarning: %v", logPrefix, errMsg) // Log as warning, continue
				} else {
					log.Printf("%sError: %v", logPrefix, errMsg)
					resultChan <- errMsg // Fail component
					return
				}
			} else {
				log.Printf("%sPrisma %s completed successfully in %s.", logPrefix, cmdInfo.name, cmdDuration)
			}
		}

		log.Printf("%sComponent finished successfully.", logPrefix)
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// waitForDB pings the database until it's available or context is cancelled.
func (e *PrismaEnv) waitForDB(ctx context.Context, databaseURL string) error {
	logPrefix := fmt.Sprintf("[%s] ", e.Name())
	log.Printf("%sWaiting for database (%s) to become available for migrations...", logPrefix, DBComponentName)
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
				log.Printf("%sError opening database connection (will retry): %v", logPrefix, err)
				continue
			}
			pingCtx, pingCancel := context.WithTimeout(timeoutCtx, 1*time.Second)
			err = db.PingContext(pingCtx)
			pingCancel()
			db.Close() // Close connection immediately after ping

			if err == nil {
				log.Printf("%sDatabase is available.", logPrefix)
				// Add a small grace period?
				time.Sleep(500 * time.Millisecond)
				return nil // Success
			}
			// Log ping error but continue waiting
			log.Printf("%sDatabase ping failed (will retry): %v", logPrefix, err)
		}
	}
}
