package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync" // Import sync
	"time"

	"github.com/gate4ai/gate4ai/server/a2a" // Use the server's a2a types
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"go.uber.org/zap"
)

// Define the key used to retrieve headers from session params
const sessionHeadersKey = "gateway_received_headers"

// DemoAgentHandler implements the A2AHandler interface.
// It parses commands from the last message in the task history and executes them sequentially.
func DemoAgentHandler(ctx context.Context, task *a2aSchema.Task, updates chan<- a2a.A2AYieldUpdate, log *zap.Logger) error {
	log.Info("Demo Agent handler started")

	// --- Send initial "working" status ---
	if err := sendStatusUpdate(ctx, updates, a2aSchema.TaskStateWorking, "Parsing commands..."); err != nil {
		log.Error("Failed to send initial working status", zap.Error(err))
		return fmt.Errorf("handler initialization failed: %w", err) // Return internal error
	}

	// --- Get the latest message ---
	if len(task.History) == 0 {
		log.Warn("Task history is empty, cannot process.")
		_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, "Cannot process task: No message found in history.")
		return errors.New("task history is empty") // Return internal error
	}
	lastMessage := task.History[len(task.History)-1]

	// --- Parse Commands ---
	commands, firstTextPart := parseCommandsFromMessage(lastMessage, log)
	log.Debug("Parsed commands", zap.Any("commands", commands), zap.String("firstTextPart", firstTextPart))

	// --- Execute Commands Sequentially ---
	var currentDelay time.Duration = 0
	var artifactSent bool = false // Track if any 'respond' command generated an artifact
	var artifactsToYield []a2aSchema.Artifact
	currentArtifactIndex := len(task.Artifacts) // Start index from existing artifacts
	var stopProcessing bool = false             // Flag to stop processing commands in this request

	for _, cmd := range commands {
		// 1. Apply Delay (if any)
		if currentDelay > 0 {
			log.Info("Applying delay", zap.Duration("duration", currentDelay))
			select {
			case <-time.After(currentDelay):
				log.Debug("Delay finished")
			case <-ctx.Done():
				log.Info("Context cancelled during wait delay")
				return ctx.Err() // Propagate cancellation
			}
			currentDelay = 0 // Reset delay
		}

		// 2. Check for Cancellation before execution
		select {
		case <-ctx.Done():
			log.Info("Context cancelled before executing command", zap.String("type", cmd.Type))
			return ctx.Err()
		default:
			// Continue
		}

		// 3. Execute Command
		log.Info("Executing command", zap.String("type", cmd.Type), zap.Any("params", cmd.Params))

		switch cmd.Type {
		case "wait":
			delaySeconds, _ := cmd.Params["duration"].(int)
			currentDelay = time.Duration(delaySeconds) * time.Second

		case "respond":
			respondType, _ := cmd.Params["respondType"].(string)
			payload, _ := cmd.Params["payload"].(string)
			var artifact a2aSchema.Artifact

			switch respondType {
			case "text":
				content := "This is the default text response."
				if payload != "" {
					content = payload
				}
				artifact = createTextArtifact("response.txt", content)
			case "file":
				name := "default.bin"
				mime := "application/octet-stream"
				bytesB64 := base64.StdEncoding.EncodeToString([]byte("DefaultFileData"))
				if payload != "" {
					// Basic validation if it looks like base64
					if _, err := base64.StdEncoding.DecodeString(payload); err == nil {
						bytesB64 = payload
						name = "payload.bin" // Assume binary if valid base64 provided
						// Could try mime type detection here based on decoded bytes if needed
					} else {
						// Treat as text payload if not valid base64
						name = "payload.txt"
						mime = "text/plain"
						bytesB64 = base64.StdEncoding.EncodeToString([]byte(payload))
						log.Warn("File payload was not valid base64, treating as text", zap.String("payload", payload))
					}
				}
				artifact = createFileArtifact(name, mime, bytesB64)
			case "data":
				dataMap := map[string]interface{}{"default": true, "value": "some mock data"}
				if payload != "" {
					var parsedData map[string]interface{}
					if err := json.Unmarshal([]byte(payload), &parsedData); err == nil {
						dataMap = parsedData
					} else {
						log.Error("Invalid JSON payload for 'respond data'", zap.String("payload", payload), zap.Error(err))
						errMsg := fmt.Sprintf("Invalid JSON payload for 'respond data': %v", err)
						_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, errMsg)
						return errors.New(errMsg) // Return internal error
					}
				}
				artifact = createDataArtifact(dataMap)
			default:
				log.Error("Unknown respond type in execution", zap.String("respondType", respondType))
				continue // Skip unknown respond type
			}

			artifact.Index = currentArtifactIndex
			currentArtifactIndex++
			artifactsToYield = append(artifactsToYield, artifact) // Collect artifacts
			artifactSent = true

		case "ask":
			promptText, _ := cmd.Params["prompt"].(string)
			if promptText == "" {
				promptText = "Please provide input:" // Default prompt
			}
			log.Info("Requesting input from user", zap.String("prompt", promptText))
			if err := sendStatusUpdate(ctx, updates, a2aSchema.TaskStateInputRequired, promptText); err != nil {
				log.Error("Failed to send input-required status", zap.Error(err))
				return fmt.Errorf("failed to send input-required status: %w", err) // Return internal error
			}
			stopProcessing = true // Stop after asking for input

		case "stream":
			chunkCount, _ := cmd.Params["count"].(int)
			log.Info("Starting stream", zap.Int("chunks", chunkCount))
			streamArtifactIndex := currentArtifactIndex
			currentArtifactIndex++ // Reserve index for the stream

			// Send initial working status for stream if needed (already sent one at start)
			// _ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateWorking, "Streaming data...")

			for i := 1; i <= chunkCount; i++ {
				select {
				case <-ctx.Done():
					log.Info("Cancellation detected during streaming")
					return ctx.Err()
				default: // Continue
				}

				time.Sleep(500 * time.Millisecond) // Simulate work

				chunkText := fmt.Sprintf("Chunk %d of %d. Timestamp: %s", i, chunkCount, time.Now().Format(time.RFC3339))
				isLastChunk := (i == chunkCount)
				isAppend := (i > 1)

				artifactChunk := a2aSchema.Artifact{
					Name:      shared.PointerTo(fmt.Sprintf("streamed_artifact_%d.txt", streamArtifactIndex)),
					Parts:     []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &chunkText}},
					Index:     streamArtifactIndex,
					Append:    shared.PointerTo(isAppend),
					LastChunk: shared.PointerTo(isLastChunk),
				}

				if err := sendArtifactUpdate(ctx, updates, artifactChunk); err != nil {
					log.Error("Failed to send stream artifact chunk", zap.Error(err), zap.Int("chunk", i))
					_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, "Failed to send stream artifact chunk.")
					return fmt.Errorf("failed to send stream artifact: %w", err)
				}
				log.Debug("Sent stream artifact chunk", zap.Int("chunk", i), zap.Int("index", streamArtifactIndex))
			}
			// Send final completed status *after* streaming artifacts
			if err := sendStatusUpdate(ctx, updates, a2aSchema.TaskStateCompleted, "Finished streaming artifacts."); err != nil {
				log.Error("Failed to send final completed status after streaming", zap.Error(err))
				// Return error as we failed to signal completion
				return fmt.Errorf("failed to send final completed status after streaming: %w", err)
			}
			log.Info("Finished streaming")
			stopProcessing = true // Stop processing commands after stream

		case "error":
			errorCode, codeOk := cmd.Params["code"].(int)
			errorType, typeOk := cmd.Params["type"].(string)

			if codeOk {
				errorMsg := fmt.Sprintf("Simulated error code %d triggered.", errorCode)
				log.Info("Triggering JSON-RPC error", zap.Int("code", errorCode))
				// Yield the error via the updates channel
				if err := sendJsonRpcErrorUpdate(ctx, updates, errorCode, errorMsg); err != nil {
					log.Error("Failed to yield JSONRPC error update", zap.Error(err))
				}
				// Also return the error from the handler function
				return &a2aSchema.JSONRPCError{Code: errorCode, Message: errorMsg}
			} else if typeOk && errorType == "fail" {
				errMsg := "simulated internal agent failure"
				log.Info(errMsg)
				_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, errMsg)
				return errors.New(errMsg) // Return internal Go error
			} else {
				errMsg := "unknown error trigger command"
				log.Warn(errMsg)
				_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, errMsg)
				return errors.New(errMsg) // Return internal Go error
			}

		case "get_headers": // NEW: Handle get_headers command
			// Retrieve headers from the session associated with the task
			// This assumes the session object is accessible or headers are stored in task metadata
			// For this example, let's assume they are in task metadata or a lookup is possible
			headers, err := getHeadersForTask(task) // Placeholder function
			if err != nil {
				log.Error("Failed to get headers for task", zap.Error(err))
				_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, "Failed to retrieve request headers.")
				return fmt.Errorf("failed to get headers: %w", err)
			}
			log.Info("Retrieved headers", zap.Any("headers", headers))
			// Create a data artifact to send the headers back
			artifact := createDataArtifact(map[string]interface{}{
				"received_headers": headers,
			})
			artifact.Name = shared.PointerTo("received_headers.json")
			artifact.Index = currentArtifactIndex
			currentArtifactIndex++
			artifactsToYield = append(artifactsToYield, artifact)
			artifactSent = true // Mark that an artifact was explicitly generated

		default:
			log.Warn("Unknown command type during execution", zap.String("type", cmd.Type))
		}

		// 4. Check for cancellation after execution
		select {
		case <-ctx.Done():
			log.Info("Context cancelled after executing command", zap.String("type", cmd.Type))
			return ctx.Err()
		default: // Continue
		}

		if stopProcessing {
			log.Info("Stopping command processing for this request", zap.String("reason", cmd.Type))
			break // Exit the command loop
		}
	} // End command loop

	// --- Yield Collected Artifacts ---
	if len(artifactsToYield) > 0 {
		log.Debug("Yielding collected artifacts", zap.Int("count", len(artifactsToYield)))
		for _, artifact := range artifactsToYield {
			if err := sendArtifactUpdate(ctx, updates, artifact); err != nil {
				log.Error("Failed to send collected artifact update", zap.Error(err))
				_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, "Failed to send results.")
				return fmt.Errorf("failed to send artifact: %w", err)
			}
		}
	}

	// --- Handle Default Artifact ---
	// Only create default artifact if NO 'respond' or 'get_headers' command was processed and we didn't stop early.
	if !artifactSent && !stopProcessing {
		log.Info("No explicit response/artifact command executed, creating default artifact.")
		defaultText := "OK"
		if firstTextPart != "" {
			// Limit length of echoed text in default response
			maxLen := 50
			if len(firstTextPart) > maxLen {
				defaultText = "OK: " + firstTextPart[:maxLen] + "..."
			} else {
				defaultText = "OK: " + firstTextPart
			}
		}
		artifact := createTextArtifact("default_response.txt", defaultText)
		artifact.Index = currentArtifactIndex // Use the next available index
		if err := sendArtifactUpdate(ctx, updates, artifact); err != nil {
			log.Error("Failed to send default artifact update", zap.Error(err))
			_ = sendStatusUpdate(ctx, updates, a2aSchema.TaskStateFailed, "Failed to send default response.")
			return fmt.Errorf("failed to send default artifact: %w", err)
		}
	}

	// --- Send Final Completed Status (if not already stopped/failed/input-required) ---
	if !stopProcessing {
		log.Info("Agent handler finished normally, sending completed status.")
		if err := sendStatusUpdate(ctx, updates, a2aSchema.TaskStateCompleted, "Task completed successfully."); err != nil {
			log.Error("Failed to send final completed status", zap.Error(err))
			return fmt.Errorf("failed to send final completed status: %w", err) // Return internal error
		}
	} else {
		log.Info("Agent handler stopped processing early due to ask/stream/error command.")
	}

	return nil // Handler finished successfully (or stopped early as expected)
}

// getHeadersForTask retrieves the headers associated with the task's session.
// Placeholder implementation: Assumes headers are stored in task.Metadata under sessionHeadersKey
// In a real scenario, this might involve looking up the session by task.SessionID.
func getHeadersForTask(task *a2aSchema.Task) (map[string]interface{}, error) {
	if task.Metadata == nil {
		return nil, fmt.Errorf("task metadata is nil, cannot retrieve headers")
	}
	headersRaw, ok := (*task.Metadata)[sessionHeadersKey]
	if !ok {
		return nil, fmt.Errorf("headers not found in task metadata under key '%s'", sessionHeadersKey)
	}

	// Attempt to type-assert the stored headers
	// Headers are likely stored as map[string]string by the transport layer
	headersMapStrStr, ok := headersRaw.(map[string]string)
	if ok {
		// Convert map[string]string to map[string]interface{}
		headersMapIf := make(map[string]interface{}, len(headersMapStrStr))
		for k, v := range headersMapStrStr {
			headersMapIf[k] = v
		}
		return headersMapIf, nil
	}

	// Try map[string]interface{} directly
	headersMapIf, ok := headersRaw.(map[string]interface{})
	if ok {
		return headersMapIf, nil
	}

	// Try sync.Map (though less likely from transport)
	headersSyncMap, ok := headersRaw.(*sync.Map)
	if ok {
		headersMapIf := make(map[string]interface{})
		headersSyncMap.Range(func(key, value interface{}) bool {
			if keyStr, okKey := key.(string); okKey {
				headersMapIf[keyStr] = value
			}
			return true // continue iteration
		})
		return headersMapIf, nil
	}

	return nil, fmt.Errorf("headers found in metadata but have unexpected type: %T", headersRaw)
}
