package a2a

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"go.uber.org/zap"
)

// ScenarioBasedA2AHandler implements the A2AHandler interface with logic based on input.
// This allows testing various A2A scenarios.
func ScenarioBasedA2AHandler(ctx context.Context, task *a2aSchema.Task, updates chan<- A2AYieldUpdate, logger *zap.Logger) error {
	log := logger.With(zap.String("taskID", task.ID))
	log.Info("Agent handler started")

	// --- Send initial "working" status ---
	if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateWorking, "Processing your request...", false); err != nil {
		return fmt.Errorf("failed to send initial working status: %w", err)
	}

	// --- Determine scenario based on the *last* user message ---
	var scenario string
	var userInput string
	if len(task.History) > 0 {
		lastMsg := task.History[len(task.History)-1]
		if lastMsg.Role == "user" && len(lastMsg.Parts) > 0 {
			// Assuming first part is text for scenario trigger
			if *lastMsg.Parts[0].Type == "text" && lastMsg.Parts[0].Text != nil {
				userInput = *lastMsg.Parts[0].Text
				inputLower := strings.ToLower(userInput)
				if strings.Contains(inputLower, "error_test") {
					scenario = "error"
				} else if strings.Contains(inputLower, "input_test") {
					scenario = "input"
				} else if strings.Contains(inputLower, "cancel_test") {
					scenario = "cancel"
				} else if strings.Contains(inputLower, "stream_test") {
					scenario = "stream"
				} else {
					scenario = "simple_success"
				}
			} else {
				scenario = "simple_success" // Default if last part isn't text
			}
		} else {
			scenario = "simple_success" // Default if last message wasn't from user
		}
	} else {
		scenario = "simple_success" // Default if no history
	}

	log.Info("Determined scenario", zap.String("scenario", scenario), zap.String("userInput", userInput))

	// --- Execute Scenario Logic ---
	switch scenario {
	case "error":
		// Simulate immediate failure
		log.Info("Simulating task failure")
		time.Sleep(500 * time.Millisecond) // Simulate some work before failing
		if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateFailed, "Simulated processing error occurred.", true); err != nil {
			log.Error("Failed to send failed status", zap.Error(err))
		}
		// Optionally return an error from the handler itself, though sending Failed status is primary
		// return errors.New("simulated task failure")

	case "input":
		// Simulate requiring user input
		log.Info("Simulating input required")
		time.Sleep(500 * time.Millisecond) // Simulate work
		// Ask the user for more details
		if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateInputRequired, "Please provide the 'secret_code' to continue.", true); err != nil { // Final=true for this phase
			log.Error("Failed to send input-required status", zap.Error(err))
		}
		// If input was provided in the *current* message, process it
		if strings.Contains(strings.ToLower(userInput), "secret_code=123") {
			log.Info("Secret code provided, completing task.")
			if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateWorking, "Processing with secret code...", false); err != nil {
				log.Error("Failed to send working status after input", zap.Error(err))
			}
			time.Sleep(1 * time.Second)
			artifact := createTextArtifact("confirmation.txt", "Secret code accepted. Task complete.")
			if err := sendArtifactUpdate(ctx, updates, task.ID, artifact); err != nil {
				log.Error("Failed to send final artifact after input", zap.Error(err))
			}
			if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateCompleted, "Completed after receiving input.", true); err != nil {
				log.Error("Failed to send completed status after input", zap.Error(err))
			}
		} else {
			log.Info("Waiting for user input with secret code.")
			// Handler finishes here, waiting for next tasks/send
		}

	case "cancel":
		// Simulate a long-running task that checks for cancellation
		log.Info("Simulating long-running task for cancellation test")
		duration := 10 * time.Second
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		startTime := time.Now()

		for {
			select {
			case <-ctx.Done(): // Check if the context was cancelled
				log.Info("Cancellation detected by handler.")
				// Send Canceled status - Capability layer handles this primarily,
				// but handler could send a specific message if needed.
				// The capability will save the final canceled state.
				return ctx.Err() // Return context error to signal cancellation
			case <-ticker.C:
				elapsed := time.Since(startTime)
				progressMsg := fmt.Sprintf("Working... (%s / %s)", elapsed.Round(time.Second), duration)
				if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateWorking, progressMsg, false); err != nil {
					log.Error("Failed to send progress update", zap.Error(err))
					// Continue? Or fail? Let's continue for this example.
				}
				if elapsed >= duration {
					log.Info("Long-running task finished normally (not cancelled).")
					artifact := createTextArtifact("result.txt", "Long task completed successfully.")
					if err := sendArtifactUpdate(ctx, updates, task.ID, artifact); err != nil {
						log.Error("Failed to send final artifact", zap.Error(err))
					}
					if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateCompleted, "Long task finished.", true); err != nil {
						log.Error("Failed to send final completed status", zap.Error(err))
					}
					return nil // Finished successfully
				}
			}
		}

	case "stream":
		// Simulate generating multiple artifacts over time
		log.Info("Simulating artifact streaming")
		for i := 0; i < 3; i++ {
			// Check for cancellation between artifacts
			select {
			case <-ctx.Done():
				log.Info("Cancellation detected during streaming")
				return ctx.Err()
			default:
				// Continue
			}

			time.Sleep(700 * time.Millisecond) // Simulate work
			fileName := fmt.Sprintf("streamed_file_%d.txt", i+1)
			content := fmt.Sprintf("This is content for file %d.\nTimestamp: %s", i+1, time.Now().Format(time.RFC3339))
			artifact := createTextArtifact(fileName, content)
			artifact.Index = i // Assign index
			if err := sendArtifactUpdate(ctx, updates, task.ID, artifact); err != nil {
				log.Error("Failed to send stream artifact update", zap.Error(err), zap.Int("index", i))
				// Decide whether to stop or continue on send error
				// Let's try to send final status even if artifact fails
				break
			}
			log.Debug("Sent stream artifact", zap.Int("index", i))
		}
		// Send final completed status after streaming artifacts
		if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateCompleted, "Finished streaming artifacts.", true); err != nil {
			log.Error("Failed to send final completed status after streaming", zap.Error(err))
		}

	case "simple_success":
		fallthrough // Execute default success logic
	default:
		// Simple success case: generate one artifact
		log.Info("Executing simple success scenario")
		time.Sleep(1 * time.Second) // Simulate work
		responseText := fmt.Sprintf("Hello from the A2A Test Agent! You said: '%s'", userInput)
		artifact := createTextArtifact("response.txt", responseText)
		if err := sendArtifactUpdate(ctx, updates, task.ID, artifact); err != nil {
			log.Error("Failed to send success artifact", zap.Error(err))
			// Proceed to send final status anyway? Yes.
		}
		if err := sendStatusUpdate(ctx, updates, task.ID, a2aSchema.TaskStateCompleted, "Task completed successfully.", true); err != nil {
			log.Error("Failed to send final completed status", zap.Error(err))
		}
	}

	log.Info("Agent handler finished")
	return nil // Handler finished (final status sent via updates channel)
}

// --- Helper Functions ---

func sendUpdate(ctx context.Context, updates chan<- A2AYieldUpdate, update A2AYieldUpdate) error {
	select {
	case updates <- update:
		return nil
	case <-ctx.Done():
		return ctx.Err() // Context cancelled
	}
}

func sendStatusUpdate(ctx context.Context, updates chan<- A2AYieldUpdate, taskID string, state a2aSchema.TaskState, messageText string, final bool) error {
	status := a2aSchema.TaskStatus{
		State:     state,
		Timestamp: time.Now(), // Use ISO8601 string
	}
	if messageText != "" {
		status.Message = &a2aSchema.Message{
			Role:  "agent",
			Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &messageText}},
		}
	}
	// NOTE: The 'final' flag for the *event* is handled by the capability layer when sending SSE.
	// The handler logic itself doesn't need to worry about the event's 'final' flag, only the task's terminal state.
	return sendUpdate(ctx, updates, A2AYieldUpdate{Status: &status})
}

func sendArtifactUpdate(ctx context.Context, updates chan<- A2AYieldUpdate, taskID string, artifact a2aSchema.Artifact) error {
	// Here, artifact.LastChunk should be set appropriately by the creator function if needed.
	return sendUpdate(ctx, updates, A2AYieldUpdate{Artifact: &artifact})
}

func createTextArtifact(name, content string) a2aSchema.Artifact {
	return a2aSchema.Artifact{
		Name:      &name,
		Parts:     []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &content}},
		Index:     0,
		LastChunk: shared.PointerTo(true),
	}
}

func createFileArtifact(name, mimeType, base64Content string) a2aSchema.Artifact {
	return a2aSchema.Artifact{
		Name: &name,
		Parts: []a2aSchema.Part{{
			Type: shared.PointerTo("file"),
			File: &a2aSchema.FileContent{
				Name:     &name,
				MimeType: &mimeType,
				Bytes:    &base64Content,
			},
		}},
		Index:     0,
		LastChunk: shared.PointerTo(true),
	}
}

// Helper function (if needed) to create a simple FilePart from text content
func createTextAsFilePart(name, textContent string) a2aSchema.Part {
	t := "file"
	mime := "text/plain"
	b64 := base64.StdEncoding.EncodeToString([]byte(textContent))
	return a2aSchema.Part{
		Type: &t,
		File: &a2aSchema.FileContent{
			Name:     &name,
			MimeType: &mime,
			Bytes:    &b64,
		},
	}
}

func createErrorStatus(err error) a2aSchema.TaskStatus {
	errMsg := "An internal error occurred."
	if err != nil {
		errMsg = err.Error()
	}
	return a2aSchema.TaskStatus{
		State:     a2aSchema.TaskStateFailed,
		Timestamp: time.Now(),
		Message: &a2aSchema.Message{
			Role:  "agent",
			Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &errMsg}},
		},
	}
}
