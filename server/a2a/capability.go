package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	// Needed for manager interface dependency
	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	mcpSchema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"

	"go.uber.org/zap"
)

// Ensure A2ACapability implements IServerCapability
var _ shared.IServerCapability = (*A2ACapability)(nil)

// --- A2A Capability ---

// A2ACapability handles A2A protocol methods (tasks/*). It acts as the bridge
// between the transport layer and the specific agent logic (A2AHandler).
type A2ACapability struct {
	logger       *zap.Logger
	manager      transport.ISessionManager // To interact with sessions for SSE/Resubscribe
	taskStore    TaskStore  // Interface for task persistence
	agentHandler A2AHandler // The actual agent logic implementation
	handlers     map[string]func(*shared.Message) (interface{}, error)
	// Track running handlers for cancellation
	runningHandlersMu sync.Mutex
	runningHandlers   map[string]context.CancelFunc // taskID -> cancelFunc
}

// NewA2ACapability creates a new A2A capability.
func NewA2ACapability(
	logger *zap.Logger,
	manager transport.ISessionManager, // Manager is needed for SSE/Resubscribe
	store TaskStore,
	handler A2AHandler,
) *A2ACapability {
	// While A2A *could* potentially operate without MCP/SSE, the current architecture
	// relies on the manager for session handling, especially for sendSubscribe/resubscribe.
	if manager == nil {
		log.Fatal("A2ACapability requires a non-nil ISessionManager for current implementation")
	}
	if store == nil {
		log.Fatal("A2ACapability requires a non-nil TaskStore")
	}
	if handler == nil {
		log.Fatal("A2ACapability requires a non-nil A2AHandler")
	}
	ac := &A2ACapability{
		logger:          logger.Named("a2a-capability"),
		manager:         manager,
		taskStore:       store,
		agentHandler:    handler,
		runningHandlers: make(map[string]context.CancelFunc),
	}
	// Map JSON-RPC method names to handler functions within this capability
	ac.handlers = map[string]func(*shared.Message) (interface{}, error){
		"tasks/send":                 ac.handleTaskSend,
		"tasks/sendSubscribe":        ac.handleTaskSendSubscribe,
		"tasks/get":                  ac.handleTaskGet,
		"tasks/cancel":               ac.handleTaskCancel,
		"tasks/pushNotification/set": ac.handleTaskPushNotificationSet, // Returns unsupported
		"tasks/pushNotification/get": ac.handleTaskPushNotificationGet, // Returns unsupported
		"tasks/resubscribe":          ac.handleTaskResubscribe,         // Basic implementation
	}
	return ac
}

// GetHandlers returns the map of JSON-RPC method handlers this capability provides.
func (ac *A2ACapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return ac.handlers
}

// --- A2A Method Handlers ---

// handleTaskSend handles synchronous task requests (`tasks/send`).
func (ac *A2ACapability) handleTaskSend(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/send"))

	var params a2aSchema.TaskSendParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/send params", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()}
	}
	// Add taskID to logger *after* parsing params
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/send request")

	// --- Load or Create Task State ---
	task, err := ac.loadOrCreateTask(context.Background(), params.ID, msg.Session.GetID(), params.Metadata)
	if err != nil {
		logger.Error("Failed to load/create task", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to initialize task"}
	}

	// --- Prevent Concurrent Execution ---
	if ac.isHandlerRunning(task.ID) {
		logger.Warn("Received tasks/send for already active task")
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidRequest, Message: "Task is already processing"}
	}

	// --- Handle Task Continuation/Restart ---
	if task.Status.State == a2aSchema.TaskStateInputRequired && params.Message.Role == "user" {
		logger.Info("Continuing task from input-required state")
		task.Status.State = a2aSchema.TaskStateSubmitted // Reset state to allow agent processing
	} else if isTerminalState(task.Status.State) && params.Message.Role == "user" {
		logger.Info("Restarting completed/failed/canceled task", zap.String("previousState", string(task.Status.State)))
		task.Status = a2aSchema.TaskStatus{State: a2aSchema.TaskStateSubmitted, Timestamp: time.Now()}
		task.Artifacts = []a2aSchema.Artifact{} // Clear previous artifacts on restart
		// Keep history for context
	}

	// --- Add User Message to History ---
	if params.Message.Role == "user" {
		if task.History == nil {
			task.History = []a2aSchema.Message{}
		}
		task.History = append(task.History, params.Message)
	}

	// --- Save Task State Before Starting Handler ---
	if err := ac.taskStore.Save(context.Background(), task); err != nil {
		logger.Error("Failed to save task state before handler start", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save task state"}
	}

	// --- Prepare and Run Handler Synchronously ---
	handlerCtx, cancel := context.WithCancel(context.Background())
	ac.storeCancelFunc(task.ID, cancel) // Store cancel func for potential task cancellation
	defer ac.removeCancelFunc(task.ID)  // Ensure cleanup when this function returns

	updates := make(chan A2AYieldUpdate, 20) // Buffered channel for agent updates
	handlerErrChan := make(chan error, 1)    // Channel for handler's final return error
	handlerLogger := logger                  // Pass logger with task context

	// Run the agent handler in a separate goroutine
	go func(currentTaskState *a2aSchema.Task) {
		defer close(handlerErrChan) // Signal completion by closing the channel
		defer close(updates)        // Close updates channel when handler goroutine finishes
		// Pass the task *as saved just before the call*
		handlerErr := ac.agentHandler(handlerCtx, currentTaskState, updates, handlerLogger)
		handlerErrChan <- handlerErr // Send final error (or nil) back
	}(task) // Pass the current task state

	// --- Process Updates from Handler Synchronously ---
	var lastTaskState *a2aSchema.Task = task // Track the latest known state
	var handlerError error                   // Store error returned by the handler goroutine
	var finalJsonRpcError *shared.JSONRPCError

	keepProcessing := true
	for keepProcessing {
		select {
		case update, ok := <-updates:
			if !ok {
				// Updates channel closed, handler finished or panicked
				logger.Debug("Updates channel closed by handler.")
				keepProcessing = false
				break // Exit select
			}
			logger.Debug("Received update from handler", zap.Any("update", update)) // Log received update

			// Apply the update yielded by the handler
			var applyErr error
			lastTaskState, applyErr = ac.applyUpdateToTask(lastTaskState, update)
			if applyErr != nil {
				logger.Error("Internal error applying update from handler", zap.Error(applyErr), zap.Any("update", update))
				lastTaskState.Status = createErrorStatus(applyErr, nil) // Mark task failed due to internal error
				handlerError = applyErr                                 // Record internal error
				cancel()                                                // Cancel handler context
				keepProcessing = false                                  // Stop processing further updates
				break                                                   // Exit select
			}

			// Check if the update itself contained a specific JSONRPCError
			if update.Error != nil {
				logger.Warn("Handler yielded a JSONRPCError", zap.Any("error", update.Error))
				lastTaskState.Status = createErrorStatus(update.Error, update.Error) // Mark task failed
				handlerError = update.Error                                          // Store the specific JSONRPCError
				finalJsonRpcError = shared.NewJSONRPCError(update.Error)             // Prepare error for client
				cancel()
				keepProcessing = false
				break
			}

			// Save intermediate state only if it's significant (input-required or terminal)
			currentState := lastTaskState.Status.State
			if currentState == a2aSchema.TaskStateInputRequired || isTerminalState(currentState) {
				if err := ac.taskStore.Save(context.Background(), lastTaskState); err != nil {
					logger.Error("Failed to save intermediate task state", zap.Error(err), zap.String("state", string(currentState)))
					// If save fails, consider it an internal error
					handlerError = fmt.Errorf("failed to save state (%s): %w", currentState, err)
					lastTaskState.Status = createErrorStatus(handlerError, nil)
					cancel()
					keepProcessing = false
				} else {
					// Successfully saved significant state, stop processing for this sync call
					logger.Debug("Saved significant intermediate state, stopping sync processing", zap.String("state", string(currentState)))
					keepProcessing = false
				}
			}

		case errFromHandler := <-handlerErrChan:
			// Handler goroutine finished, capture its return error
			logger.Debug("Handler goroutine finished", zap.Error(errFromHandler))
			handlerError = errFromHandler
			// Drain any remaining updates that might have been sent just before finishing
			// Keep processing variable set to false here to prevent re-entering loop after handler error
			keepProcessing = false // Stop main processing loop
		}
	} // End processing loop

	// Drain any remaining updates that might have been sent just before finishing,
	// *after* the main loop has exited.
	logger.Debug("Draining remaining updates channel after main loop")
	for update := range updates { // Read until the channel is closed and empty
		logger.Debug("Draining remaining update after handler finished", zap.Any("update", update))
		var applyErr error
		lastTaskState, applyErr = ac.applyUpdateToTask(lastTaskState, update)
		if applyErr != nil {
			// Handle potential error during draining, maybe log and mark task as failed
			logger.Error("Error applying drained update", zap.Error(applyErr), zap.Any("update", update))
			// Prioritize the original handlerError if it exists
			if handlerError == nil {
				handlerError = fmt.Errorf("failed applying drained update: %w", applyErr) // Record error
			}
			lastTaskState.Status = createErrorStatus(handlerError, nil) // Use potentially updated handlerError
			break                                                       // Stop draining on error
		}
		// Also check for yielded errors during drain
		if update.Error != nil {
			logger.Error("Handler yielded JSONRPCError during drain", zap.Any("error", update.Error))
			if handlerError == nil { // Prioritize original error
				handlerError = update.Error
			}
			lastTaskState.Status = createErrorStatus(handlerError, update.Error)
			// Prepare error for client only if no specific error was already set
			if finalJsonRpcError == nil {
				finalJsonRpcError = shared.NewJSONRPCError(update.Error)
			}
			break // Stop draining on error
		}
	}
	logger.Debug("Finished draining updates channel")

	// --- Final State Handling and Response Preparation ---
	// Ensure task is in a final state if the handler finished without error or yielding input-required/terminal
	if handlerError == nil && lastTaskState.Status.State != a2aSchema.TaskStateInputRequired && !isTerminalState(lastTaskState.Status.State) {
		logger.Warn("Handler finished successfully but task not in terminal/input state. Setting to 'completed'.", zap.String("finalState", string(lastTaskState.Status.State)))
		lastTaskState.Status.State = a2aSchema.TaskStateCompleted
		lastTaskState.Status.Timestamp = time.Now()
	} else if handlerError != nil && !errors.Is(handlerError, context.Canceled) {
		// If handler returned an error (and it wasn't cancellation)
		logger.Error("Agent handler finished with an error", zap.Error(handlerError))
		// If it wasn't already set by yielding an error update, set status to failed
		if lastTaskState.Status.State != a2aSchema.TaskStateFailed {
			if jsonErr, ok := handlerError.(*a2aSchema.JSONRPCError); ok {
				lastTaskState.Status = createErrorStatus(jsonErr, jsonErr)
				if finalJsonRpcError == nil { // Don't overwrite specific error yielded during drain/main loop
					finalJsonRpcError = shared.NewJSONRPCError(jsonErr) // Prepare error for client
				}
			} else {
				lastTaskState.Status = createErrorStatus(handlerError, nil) // Generic internal error status
				// Prepare generic internal error for client if none set yet
				if finalJsonRpcError == nil {
					finalJsonRpcError = &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Internal agent error occurred"}
				}
			}
		}
	} else if errors.Is(handlerError, context.Canceled) {
		logger.Info("Handler execution cancelled", zap.Error(handlerError))
		// Update status only if not already Canceled (e.g., by handleTaskCancel)
		if lastTaskState.Status.State != a2aSchema.TaskStateCanceled {
			lastTaskState.Status.State = a2aSchema.TaskStateCanceled
			lastTaskState.Status.Timestamp = time.Now()
		}
	}

	// --- Save the final determined state ---
	if err := ac.taskStore.Save(context.Background(), lastTaskState); err != nil {
		logger.Error("Failed to save final task state", zap.Error(err))
		// If final save fails, return internal error to client
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save final task state"}
	}

	// --- Return Result or Error to Client ---
	if finalJsonRpcError != nil {
		// Return the specific error determined during processing
		logger.Info("Returning error to client", zap.Int("code", finalJsonRpcError.Code), zap.String("message", finalJsonRpcError.Message))
		return nil, finalJsonRpcError
	}

	// --- Prepare Successful Response ---
	finalResponseTask := *lastTaskState // Copy final state for response
	// Handle history length trimming
	if params.HistoryLength != nil && *params.HistoryLength >= 0 {
		historyLen := *params.HistoryLength
		if len(finalResponseTask.History) > historyLen {
			finalResponseTask.History = finalResponseTask.History[len(finalResponseTask.History)-historyLen:]
		}
	} else {
		finalResponseTask.History = nil // Omit history if not requested or negative length
	}

	logger.Debug("tasks/send completed successfully", zap.String("finalState", string(finalResponseTask.Status.State)))
	return &finalResponseTask, nil // Return the final task object
}

// handleTaskSendSubscribe handles asynchronous task requests with SSE streaming (`tasks/sendSubscribe`).
func (ac *A2ACapability) handleTaskSendSubscribe(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/sendSubscribe"))

	var params a2aSchema.TaskSendParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/sendSubscribe params", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()}
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/sendSubscribe request")

	// --- Load or Create Task ---
	task, err := ac.loadOrCreateTask(context.Background(), params.ID, msg.Session.GetID(), params.Metadata)
	if err != nil {
		logger.Error("Failed to load/create task", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to initialize task"}
	}

	// --- Prevent Concurrent Execution ---
	if ac.isHandlerRunning(task.ID) {
		logger.Warn("Received tasks/sendSubscribe for already active task")
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidRequest, Message: "Task is already processing, use tasks/resubscribe"}
	}

	// --- Handle Task Continuation/Restart ---
	if task.Status.State == a2aSchema.TaskStateInputRequired && params.Message.Role == "user" {
		logger.Info("Continuing task from input-required state via sendSubscribe")
		task.Status.State = a2aSchema.TaskStateSubmitted
	} else if isTerminalState(task.Status.State) && params.Message.Role == "user" {
		logger.Info("Restarting completed/failed/canceled task via sendSubscribe", zap.String("previousState", string(task.Status.State)))
		task.Status = a2aSchema.TaskStatus{State: a2aSchema.TaskStateSubmitted, Timestamp: time.Now()}
		task.Artifacts = []a2aSchema.Artifact{}
	}

	// --- Add User Message to History ---
	if params.Message.Role == "user" {
		if task.History == nil {
			task.History = []a2aSchema.Message{}
		}
		task.History = append(task.History, params.Message)
	}

	// --- Save Task State Before Starting Handler ---
	if err := ac.taskStore.Save(context.Background(), task); err != nil {
		logger.Error("Failed to save task state before handler start (sendSubscribe)", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save task state"}
	}

	// --- Prepare and Start Handler Asynchronously ---
	handlerCtx, cancel := context.WithCancel(context.Background())
	ac.storeCancelFunc(task.ID, cancel)      // Store cancel func
	updates := make(chan A2AYieldUpdate, 20) // Buffered channel for agent updates
	handlerLogger := logger                  // Pass logger with task context

	var wait4TaskUpdates sync.WaitGroup
	wait4TaskUpdates.Add(1)

	// Goroutine to run the agent's logic
	go func(initialTaskState *a2aSchema.Task) {
		defer ac.removeCancelFunc(task.ID) // Remove cancel func ref when handler exits
		defer close(updates)               // Close updates chan when handler exits

		handlerErr := ac.agentHandler(handlerCtx, initialTaskState, updates, handlerLogger)
		wait4TaskUpdates.Wait()

		// --- Handle Handler Completion/Error (after update processing finishes) ---
		if handlerErr != nil && !errors.Is(handlerErr, context.Canceled) {
			logger.Error("Agent handler returned an error during streaming", zap.Error(handlerErr))
			// Determine final error status and event
			var finalStatus a2aSchema.TaskStatus
			var finalEvent *shared.A2AStreamEvent
			if jsonRPCErr, ok := handlerErr.(*a2aSchema.JSONRPCError); ok {
				finalStatus = createErrorStatus(jsonRPCErr, jsonRPCErr)
				finalEvent = &shared.A2AStreamEvent{Type: "error", Error: jsonRPCErr, Final: true}
			} else {
				finalStatus = createErrorStatus(handlerErr, nil) // Generic internal error
				finalEvent = &shared.A2AStreamEvent{
					Type:   "status",
					Status: &a2aSchema.TaskStatusUpdateEvent{ID: task.ID, Status: finalStatus, Final: true},
					Final:  true,
				}
			}
			// Send final event via session
			if sendErr := msg.Session.SendA2AStreamEvent(finalEvent); sendErr != nil {
				logger.Error("Failed to send final failed status/error event", zap.Error(sendErr))
			}
			// Save final failed state
			finalTaskState, loadErr := ac.taskStore.Load(context.Background(), task.ID)
			if loadErr != nil {
				finalTaskState = task // Fallback
			}
			finalTaskState.Status = finalStatus
			if saveErr := ac.taskStore.Save(context.Background(), finalTaskState); saveErr != nil {
				logger.Error("Failed to save final failed task state", zap.Error(saveErr))
			}
		} else if errors.Is(handlerErr, context.Canceled) {
			logger.Info("Handler execution cancelled", zap.Error(handlerErr))
			// Cancellation status should be set by handleTaskCancel or transport disconnect cleanup
		} else {
			logger.Debug("Agent handler finished processing stream normally")
			// Ensure task completion if not already terminal/input-required
			finalTaskState, loadErr := ac.taskStore.Load(context.Background(), task.ID)
			if loadErr != nil {
				logger.Error("Failed to load task state after handler completion", zap.Error(loadErr))
			} else if !isTerminalState(finalTaskState.Status.State) && finalTaskState.Status.State != a2aSchema.TaskStateInputRequired {
				logger.Warn("Handler finished but task not in terminal/input state. Sending 'completed'.")
				finalStatus := a2aSchema.TaskStatus{
					State:     a2aSchema.TaskStateCompleted,
					Timestamp: time.Now(),
					Message:   finalTaskState.Status.Message,
				}
				finalEvent := &shared.A2AStreamEvent{
					Type:   "status",
					Status: &a2aSchema.TaskStatusUpdateEvent{ID: task.ID, Status: finalStatus, Final: true},
					Final:  true,
				}
				if sendErr := msg.Session.SendA2AStreamEvent(finalEvent); sendErr != nil {
					logger.Error("Failed to send final completed status event", zap.Error(sendErr))
				}
				finalTaskState.Status = finalStatus
				if saveErr := ac.taskStore.Save(context.Background(), finalTaskState); saveErr != nil {
					logger.Error("Failed to save final completed task state", zap.Error(saveErr))
				}
			}
		}
	}(task) // Pass the task state

	// Goroutine to process updates from the handler and send SSE events via the session
	go func(currentTaskState *a2aSchema.Task) {
		defer wait4TaskUpdates.Done()

		lastTaskState := currentTaskState // Track state locally for saving
		isFinalEventSent := false

		for update := range updates { // Read from updates channel until closed
			// Apply update to local task state copy
			var applyErr error
			lastTaskState, applyErr = ac.applyUpdateToTask(lastTaskState, update)
			if applyErr != nil {
				logger.Error("Failed to apply update to task during streaming", zap.Error(applyErr), zap.Any("update", update))
				errorEvent := &shared.A2AStreamEvent{Type: "error", Error: &a2aSchema.JSONRPCError{Code: a2aSchema.ErrorInternalError, Message: fmt.Sprintf("Internal error applying update: %v", applyErr)}, Final: false}
				_ = msg.Session.SendA2AStreamEvent(errorEvent) // Try to send error event
				continue                                       // Skip saving/sending this broken update
			}

			// Handle yielded JSONRPCError
			if update.Error != nil {
				logger.Error("Handler yielded JSONRPCError during stream", zap.Any("error", update.Error))
				errorEvent := &shared.A2AStreamEvent{Type: "error", Error: update.Error, Final: true}
				_ = msg.Session.SendA2AStreamEvent(errorEvent)
				lastTaskState.Status = createErrorStatus(update.Error, update.Error)           // Update local state copy
				if err := ac.taskStore.Save(context.Background(), lastTaskState); err != nil { // Save failed state
					logger.Error("Failed to save task state after yielded error", zap.Error(err))
				}
				ac.cancelHandler(task.ID) // Cancel original context
				return                    // Stop processing updates
			}

			// Save the updated task state
			if err := ac.taskStore.Save(context.Background(), lastTaskState); err != nil {
				logger.Error("Failed to save task state during streaming", zap.Error(err))
				// Consider if failure to save should stop the stream? Potentially yes.
				// Let's send an error event and stop.
				errorEvent := &shared.A2AStreamEvent{Type: "error", Error: &a2aSchema.JSONRPCError{Code: a2aSchema.ErrorInternalError, Message: fmt.Sprintf("Internal error saving state: %v", err)}, Final: true}
				_ = msg.Session.SendA2AStreamEvent(errorEvent)
				ac.cancelHandler(task.ID)
				return
			}

			// Prepare A2AStreamEvent to send to client
			var eventToSend *shared.A2AStreamEvent
			isFinal := isTerminalState(lastTaskState.Status.State) || lastTaskState.Status.State == a2aSchema.TaskStateInputRequired

			if update.Status != nil {
				eventToSend = &shared.A2AStreamEvent{
					Type: "status",
					Status: &a2aSchema.TaskStatusUpdateEvent{
						ID:     task.ID,
						Status: *update.Status,
						Final:  isFinal,
					},
					Final: isFinal,
				}
			} else if update.Artifact != nil {
				eventToSend = &shared.A2AStreamEvent{
					Type: "artifact",
					Artifact: &a2aSchema.TaskArtifactUpdateEvent{
						ID:       task.ID,
						Artifact: *update.Artifact,
					},
					Final: false,
				}
			}

			// Send event via session's output channel
			if eventToSend != nil {
				if err := msg.Session.SendA2AStreamEvent(eventToSend); err != nil {
					logger.Error("Failed to send A2A stream event, cancelling handler", zap.Error(err))
					ac.cancelHandler(task.ID) // Cancel the agent handler
					return                    // Stop processing updates
				}
				if isFinal {
					isFinalEventSent = true
					logger.Debug("Sent final event via SSE", zap.String("state", string(lastTaskState.Status.State)))
					// Don't return yet, handler goroutine manages final cleanup.
				}
			}
		} // End update processing loop

		logger.Debug("Update processing loop finished for task", zap.String("taskID", task.ID))
		if !isFinalEventSent {
			logger.Debug("No final event was sent explicitly during update processing (handler might send one)")
		}
	}(task) // End update processing goroutine

	// For tasks/sendSubscribe, the initial JSON-RPC response acknowledges the request initiation.
	// Return the initial task state (without artifacts, potentially trimmed history).
	initialResponseTask := *task // Copy initial task state
	if params.HistoryLength != nil && *params.HistoryLength >= 0 {
		historyLen := *params.HistoryLength
		if len(initialResponseTask.History) > historyLen {
			initialResponseTask.History = initialResponseTask.History[len(initialResponseTask.History)-historyLen:]
		}
	} else {
		initialResponseTask.History = nil // No history requested
	}
	initialResponseTask.Artifacts = nil // Artifacts are sent via SSE events

	logger.Debug("tasks/sendSubscribe initiated, returning initial task state", zap.String("initialState", string(initialResponseTask.Status.State)))
	return &initialResponseTask, nil
}

// handleTaskGet handles `tasks/get` requests.
func (ac *A2ACapability) handleTaskGet(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/get"))

	var params a2aSchema.TaskQueryParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/get params", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()}
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/get request")

	task, err := ac.taskStore.Load(context.Background(), params.ID)
	if err != nil {
		logger.Warn("Failed to load task for get", zap.Error(err))
		var jsonRPCErr *a2aSchema.JSONRPCError
		if errors.As(err, &jsonRPCErr) {
			return nil, shared.NewJSONRPCError(jsonRPCErr) // Propagate specific errors like TaskNotFound
		}
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to load task state"}
	}

	// --- Handle History Length ---
	responseTask := *task // Copy task before modification
	if params.HistoryLength != nil && *params.HistoryLength >= 0 {
		historyLen := *params.HistoryLength
		if len(responseTask.History) > historyLen {
			responseTask.History = responseTask.History[len(responseTask.History)-historyLen:]
		} // Keep history as is if length is sufficient or 0 requested
	} else {
		responseTask.History = nil // Omit history if not requested or negative length
	}

	logger.Debug("Returning task state", zap.String("state", string(responseTask.Status.State)))
	return &responseTask, nil
}

// handleTaskCancel handles `tasks/cancel` requests.
func (ac *A2ACapability) handleTaskCancel(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/cancel"))

	var params a2aSchema.TaskIdParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/cancel params", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()}
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/cancel request")

	task, err := ac.taskStore.Load(context.Background(), params.ID)
	if err != nil {
		logger.Warn("Failed to load task for cancellation", zap.Error(err))
		var jsonRPCErr *a2aSchema.JSONRPCError
		if errors.As(err, &jsonRPCErr) {
			return nil, shared.NewJSONRPCError(jsonRPCErr)
		}
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to load task state"}
	}

	// --- Check if Cancellable ---
	if isTerminalState(task.Status.State) {
		logger.Warn("Task already in terminal state, cannot cancel", zap.String("state", string(task.Status.State)))
		return nil, shared.NewJSONRPCError(a2aSchema.NewTaskNotCancelableError(params.ID))
	}

	// --- Cancel Running Handler ---
	wasRunning := ac.cancelHandler(params.ID) // This calls cancel() and removes from map
	if !wasRunning {
		logger.Warn("Cancel requested, but no running handler found (might have finished between load and cancel)")
	}

	// --- Update Task State ---
	task.Status.State = a2aSchema.TaskStateCanceled
	task.Status.Timestamp = time.Now()
	cancelMsgText := "Task canceled by client request."
	task.Status.Message = &a2aSchema.Message{Role: "agent", Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &cancelMsgText}}}

	// --- Save Final Canceled State ---
	if err := ac.taskStore.Save(context.Background(), task); err != nil {
		logger.Error("Failed to save canceled task state", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save canceled task state"}
	}

	// The transport layer's SSE handler (`streamA2AResponse`) associated with the *original*
	// `sendSubscribe` request should detect the context cancellation triggered by `cancelHandler`
	// and terminate the SSE stream gracefully. No explicit SSE event needed here.

	logger.Debug("Task canceled successfully")
	// Return the updated task state (without history)
	responseTask := *task
	responseTask.History = nil
	responseTask.Artifacts = nil // Don't return artifacts for cancel response
	return &responseTask, nil
}

// handleTaskPushNotificationSet returns "Unsupported Operation".
func (ac *A2ACapability) handleTaskPushNotificationSet(msg *shared.Message) (interface{}, error) {
	return nil, shared.NewJSONRPCError(a2aSchema.NewUnsupportedOperationError("tasks/pushNotification/set"))
}

// handleTaskPushNotificationGet returns "Unsupported Operation".
func (ac *A2ACapability) handleTaskPushNotificationGet(msg *shared.Message) (interface{}, error) {
	return nil, shared.NewJSONRPCError(a2aSchema.NewUnsupportedOperationError("tasks/pushNotification/get"))
}

// handleTaskResubscribe handles `tasks/resubscribe` requests.
// Note: Full resumption of live updates from an *existing* handler run is complex.
// This implementation provides a snapshot and starts a *new* stream that won't get
// updates from the original handler instance.
func (ac *A2ACapability) handleTaskResubscribe(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/resubscribe"))

	var params a2aSchema.TaskQueryParams // Resubscribe uses TaskQueryParams according to spec example
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/resubscribe params", zap.Error(err))
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()}
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/resubscribe request")

	// --- Load Task State ---
	task, err := ac.taskStore.Load(context.Background(), params.ID)
	if err != nil {
		logger.Warn("Failed to load task for resubscribe", zap.Error(err))
		var jsonRPCErr *a2aSchema.JSONRPCError
		if errors.As(err, &jsonRPCErr) {
			return nil, shared.NewJSONRPCError(jsonRPCErr)
		}
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to load task state"}
	}

	// --- Handle History Length for Initial Response ---
	responseTask := *task // Copy before modification
	if params.HistoryLength != nil && *params.HistoryLength >= 0 {
		historyLen := *params.HistoryLength
		if len(responseTask.History) > historyLen {
			responseTask.History = responseTask.History[len(responseTask.History)-historyLen:]
		}
	} else {
		responseTask.History = nil
	}
	// Don't include artifacts in the initial resubscribe response.
	// The client should use tasks/get if they need the full current artifact state.
	responseTask.Artifacts = nil

	// --- Check Task Status for Response/Stream Behavior ---
	if isTerminalState(task.Status.State) {
		logger.Info("Task already terminated, returning final state for resubscribe", zap.String("state", string(task.Status.State)))
		// The transport layer should see the terminal state and send *only* the final status event.
		// We return the task object containing the terminal status.
		return &responseTask, nil
	}

	if !ac.isHandlerRunning(task.ID) {
		// Task is not terminal, but handler isn't running (inconsistent state).
		logger.Error("Resubscribe requested for non-terminal task with no running handler", zap.String("state", string(task.Status.State)))
		// Mark task as failed and return error
		task.Status = createErrorStatus(errors.New("inconsistent state: task not terminal but handler not running"), nil)
		_ = ac.taskStore.Save(context.Background(), task) // Attempt to save error state
		return nil, &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Task in inconsistent state, cannot resubscribe"}
	}

	// --- Task is Running - Provide Current State ---
	// The transport layer will start an SSE stream upon seeing the Accept header.
	// This response provides the initial state snapshot for the resubscribing client.
	// WARNING: Updates from the original agent handler run will NOT be sent to this new stream.
	logger.Warn("Resuming stream not fully implemented. Returning current state. New updates from the original handler run won't be sent to this new stream.")
	return &responseTask, nil

	// Proper resumption would require complex state management (sharing update channels, fan-out).
	// return nil, shared.NewJSONRPCError(a2aSchema.NewUnsupportedOperationError("tasks/resubscribe (full streaming resumption not implemented)"))
}

// --- Helper Methods ---

// loadOrCreateTask retrieves a task or creates a new one if not found.
func (ac *A2ACapability) loadOrCreateTask(ctx context.Context, taskID string, sessionID string, metadata *map[string]interface{}) (*a2aSchema.Task, error) {
	task, err := ac.taskStore.Load(ctx, taskID)
	if err == nil { // Task found
		ac.logger.Debug("Loaded existing task", zap.String("taskID", taskID), zap.String("state", string(task.Status.State)))
		// Update metadata if provided in the current request? Let's merge/overwrite.
		if metadata != nil {
			task.Metadata = metadata // Replace metadata
		}
		// Should we update SessionID if provided? Let's keep the original SessionID.
		return task, nil
	}

	// Check if error is specifically TaskNotFoundError
	var jsonRPCErr *a2aSchema.JSONRPCError
	if errors.As(err, &jsonRPCErr) && jsonRPCErr.Code == a2aSchema.ErrorCodeTaskNotFound {
		// Task not found, proceed to create a new one
		ac.logger.Info("Task not found, creating new task", zap.String("taskID", taskID))
		newTask := &a2aSchema.Task{
			ID:        taskID,
			SessionID: sessionID,
			Status: a2aSchema.TaskStatus{
				State:     a2aSchema.TaskStateSubmitted,
				Timestamp: time.Now(),
			},
			Artifacts: []a2aSchema.Artifact{}, // Initialize slices
			History:   []a2aSchema.Message{},
			Metadata:  metadata, // Set initial metadata
		}
		if err := ac.taskStore.Save(ctx, newTask); err != nil {
			ac.logger.Error("Failed to save newly created task", zap.String("taskID", taskID), zap.Error(err))
			return nil, fmt.Errorf("failed to save newly created task: %w", err)
		}
		return newTask, nil
	}

	// Unexpected error loading task
	ac.logger.Error("Unexpected error loading task", zap.String("taskID", taskID), zap.Error(err))
	return nil, fmt.Errorf("internal error loading task: %w", err) // Return internal error
}

// applyUpdateToTask modifies the task based on the yielded update from the handler.
// It returns a *new* task instance with the update applied.
func (ac *A2ACapability) applyUpdateToTask(task *a2aSchema.Task, update A2AYieldUpdate) (*a2aSchema.Task, error) {
	if task == nil {
		return nil, errors.New("cannot apply update to nil task")
	}

	// --- Create a Deep Copy ---
	// This prevents modifying the state shared with the handler or other potential readers.
	taskCopy := *task
	// Deep copy slices and maps within the task
	if task.Artifacts != nil {
		taskCopy.Artifacts = make([]a2aSchema.Artifact, len(task.Artifacts))
		copy(taskCopy.Artifacts, task.Artifacts)
		// Further deep copy Parts within artifacts if necessary (structs are usually copied by value)
	} else {
		taskCopy.Artifacts = []a2aSchema.Artifact{} // Ensure initialized
	}
	if task.History != nil {
		taskCopy.History = make([]a2aSchema.Message, len(task.History))
		copy(taskCopy.History, task.History)
		// Further deep copy Parts within messages if necessary
	} else {
		taskCopy.History = []a2aSchema.Message{} // Ensure initialized
	}
	if task.Metadata != nil {
		newMeta := make(map[string]interface{}, len(*task.Metadata))
		for k, v := range *task.Metadata {
			newMeta[k] = v // Shallow copy of map values is usually sufficient
		}
		taskCopy.Metadata = &newMeta
	}
	// Status is a struct, copied by value. Message inside status is a pointer, handle defensively.
	if task.Status.Message != nil {
		msgCopy := *task.Status.Message
		// Deep copy parts in status message
		if task.Status.Message.Parts != nil {
			msgCopy.Parts = make([]a2aSchema.Part, len(task.Status.Message.Parts))
			copy(msgCopy.Parts, task.Status.Message.Parts)
		}
		taskCopy.Status.Message = &msgCopy
	}

	// --- Apply Update to the Copy ---
	if update.Status != nil {
		taskCopy.Status = *update.Status
		if taskCopy.Status.Timestamp.IsZero() {
			taskCopy.Status.Timestamp = time.Now()
		}
		if taskCopy.Status.Message != nil && taskCopy.Status.Message.Role == "agent" {
			// Add agent message from status to history (avoiding duplicates is complex, let's just append)
			taskCopy.History = append(taskCopy.History, *taskCopy.Status.Message)
		}
	} else if update.Artifact != nil {
		artifactUpdate := *update.Artifact
		found := false
		for i := range taskCopy.Artifacts {
			if taskCopy.Artifacts[i].Index == artifactUpdate.Index {
				existingArtifact := &taskCopy.Artifacts[i]
				if artifactUpdate.Append != nil && *artifactUpdate.Append {
					existingArtifact.Parts = append(existingArtifact.Parts, artifactUpdate.Parts...)
					if artifactUpdate.LastChunk != nil {
						existingArtifact.LastChunk = artifactUpdate.LastChunk
					} // Update other fields as needed
				} else {
					taskCopy.Artifacts[i] = artifactUpdate // Overwrite
				}
				found = true
				break
			}
		}
		if !found {
			taskCopy.Artifacts = append(taskCopy.Artifacts, artifactUpdate) // Append new
		}
		taskCopy.Status.Timestamp = time.Now() // Update timestamp on artifact change
	} else if update.Error != nil {
		// Handler yielded an error, mark task as failed
		taskCopy.Status = createErrorStatus(update.Error, update.Error)
	} else {
		ac.logger.Warn("Received empty A2AYieldUpdate (no status, artifact, or error)")
	}

	return &taskCopy, nil
}

// storeCancelFunc stores the cancel function associated with a running task handler.
func (ac *A2ACapability) storeCancelFunc(taskID string, cancel context.CancelFunc) {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	if existingCancel, ok := ac.runningHandlers[taskID]; ok {
		ac.logger.Warn("Handler already running for task, cancelling previous one", zap.String("taskID", taskID))
		existingCancel() // Cancel the old one before storing the new one
	}
	ac.runningHandlers[taskID] = cancel
	ac.logger.Debug("Stored cancel function for task", zap.String("taskID", taskID))
}

// removeCancelFunc removes the reference to the cancel function for a task.
// This is called when the handler goroutine finishes or is explicitly cancelled.
func (ac *A2ACapability) removeCancelFunc(taskID string) {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	if _, ok := ac.runningHandlers[taskID]; ok {
		delete(ac.runningHandlers, taskID)
		ac.logger.Debug("Removed cancel function reference for task", zap.String("taskID", taskID))
	}
}

// isHandlerRunning checks if a handler is currently tracked as running for the taskID.
func (ac *A2ACapability) isHandlerRunning(taskID string) bool {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	_, ok := ac.runningHandlers[taskID]
	return ok
}

// cancelHandler explicitly cancels the running handler's context for a taskID.
// It retrieves the cancel func, removes it from the map, and then calls it.
// Returns true if a handler was found and its context cancellation was invoked.
func (ac *A2ACapability) cancelHandler(taskID string) bool {
	ac.runningHandlersMu.Lock()
	cancel, ok := ac.runningHandlers[taskID]
	if ok {
		// Remove immediately while holding lock to prevent race conditions
		delete(ac.runningHandlers, taskID)
	}
	ac.runningHandlersMu.Unlock() // Unlock *before* calling cancel

	if ok {
		ac.logger.Info("Cancelling running handler context for task", zap.String("taskID", taskID))
		cancel() // Call the actual context cancellation function
		return true
	}
	ac.logger.Debug("No running handler found to cancel for task", zap.String("taskID", taskID))
	return false
}

// SetCapabilities is part of the IServerCapability interface.
// For A2A, capabilities are primarily defined in the Agent Card, not MCP capabilities.
func (ac *A2ACapability) SetCapabilities(s *mcpSchema.ServerCapabilities) {
	ac.logger.Debug("SetCapabilities called on A2ACapability (no MCP fields modified)")
}

// isTerminalState checks if a task state indicates final completion (success, failure, or cancellation).
func isTerminalState(state a2aSchema.TaskState) bool {
	switch state {
	case a2aSchema.TaskStateCompleted, a2aSchema.TaskStateFailed, a2aSchema.TaskStateCanceled:
		return true
	default:
		return false
	}
}

// SetManager allows setting the session manager (needed for interface compatibility if used in MCP context).
func (ac *A2ACapability) SetManager(manager transport.ISessionManager) {
	ac.manager = manager
}

// createErrorStatus generates a TaskStatus for a failed state.
// If jsonErr is provided and represents a client-facing error, its details are used.
// Otherwise, a generic message based on the Go error is used.
func createErrorStatus(err error, jsonErr *a2aSchema.JSONRPCError) a2aSchema.TaskStatus {
	errMsg := "An internal agent error occurred."
	if jsonErr != nil {
		// Use the message from the specific JSONRPCError
		errMsg = jsonErr.Message
	} else if err != nil {
		// Use the Go error message for internal logging, but maybe not client response?
		// For now, let's use it for the status message.
		errMsg = fmt.Sprintf("Internal error: %v", err)
	}

	// Create the agent message containing the error part
	agentMessage := &a2aSchema.Message{
		Role: "agent",
		Parts: []a2aSchema.Part{
			{Type: shared.PointerTo("text"), Text: &errMsg},
		},
	}

	return a2aSchema.TaskStatus{
		State:     a2aSchema.TaskStateFailed,
		Timestamp: time.Now(),
		Message:   agentMessage, // Embed the error message details
	}
}