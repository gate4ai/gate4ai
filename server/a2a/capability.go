package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/server/mcp" // Needed for manager interface
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	mcpSchema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"

	// Only for SetCapabilities type
	// Only for SetCapabilities type
	"go.uber.org/zap"
)

// Ensure A2ACapability implements IServerCapability
var _ shared.IServerCapability = (*A2ACapability)(nil)

// --- A2A Task Handling Interface ---

// A2AYieldUpdate represents the possible types of updates an A2AHandler can yield.
type A2AYieldUpdate struct {
	Status   *a2aSchema.TaskStatus // Use a pointer to differentiate
	Artifact *a2aSchema.Artifact   // Use a pointer to differentiate
}

// A2AHandler defines the signature for the core logic of an A2A agent.
// It receives the task context and a channel to send updates back to the capability.
// The handler should run asynchronously and send updates via the channel.
// It should return an error if the handler initialization fails critically.
// The final task state (completed, failed) should be sent via the updates channel.
// A logger is passed for handler-specific logging.
type A2AHandler func(ctx context.Context, task *a2aSchema.Task, updates chan<- A2AYieldUpdate, logger *zap.Logger) error

// --- A2A Capability ---

// A2ACapability handles A2A protocol methods (tasks/*).
type A2ACapability struct {
	logger       *zap.Logger
	manager      mcp.ISessionManager // To interact with sessions for SSE
	taskStore    TaskStore
	agentHandler A2AHandler
	handlers     map[string]func(*shared.Message) (interface{}, error)
	// Track running handlers for cancellation
	runningHandlersMu sync.Mutex
	runningHandlers   map[string]context.CancelFunc // taskID -> cancelFunc
}

// NewA2ACapability creates a new A2A capability.
func NewA2ACapability(
	logger *zap.Logger,
	manager mcp.ISessionManager, // Manager is required now
	store TaskStore,
	handler A2AHandler,
) *A2ACapability {
	if manager == nil {
		panic("A2ACapability requires a non-nil ISessionManager")
	}
	if store == nil {
		panic("A2ACapability requires a non-nil TaskStore")
	}
	if handler == nil {
		panic("A2ACapability requires a non-nil A2AHandler")
	}
	ac := &A2ACapability{
		logger:          logger.Named("a2a-capability"),
		manager:         manager,
		taskStore:       store,
		agentHandler:    handler,
		runningHandlers: make(map[string]context.CancelFunc),
	}
	ac.handlers = map[string]func(*shared.Message) (interface{}, error){
		"tasks/send":                 ac.handleTaskSend,
		"tasks/sendSubscribe":        ac.handleTaskSendSubscribe,
		"tasks/get":                  ac.handleTaskGet,
		"tasks/cancel":               ac.handleTaskCancel,
		"tasks/pushNotification/set": ac.handleTaskPushNotificationSet,
		"tasks/pushNotification/get": ac.handleTaskPushNotificationGet,
		"tasks/resubscribe":          ac.handleTaskResubscribe, // Added Resubscribe
	}
	return ac
}

// GetHandlers returns the map of JSON-RPC method handlers.
func (ac *A2ACapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return ac.handlers
}

// --- A2A Method Handlers ---

// handleTaskSend handles synchronous task requests.
func (ac *A2ACapability) handleTaskSend(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/send"))

	var params a2aSchema.TaskSendParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/send params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()})
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/send request")

	// --- Load or Create Task ---
	task, err := ac.loadOrCreateTask(context.Background(), params.ID, params.SessionID, params.Metadata)
	if err != nil {
		logger.Error("Failed to load/create task", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to initialize task"})
	}

	// --- Check if Task is Active ---
	if ac.isHandlerRunning(task.ID) {
		logger.Warn("Received tasks/send for already active task")
		// Decide behavior: return current state? return error? For sync, let's return error.
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidRequest, Message: "Task is already processing"})
	}
	if isTerminalState(task.Status.State) && params.Message.Role == "user" {
		// If task is finished and user sends a new message, treat it as continuation/new phase
		logger.Info("Continuing completed task", zap.String("previousState", string(task.Status.State)))
		task.Status.State = a2aSchema.TaskStateSubmitted // Reset state? Or let handler decide? Let handler decide based on history.
	}

	// --- Add User Message to History ---
	if params.Message.Role == "user" { // Only add user messages here
		task.History = append(task.History, params.Message)
		// Persist the history addition immediately? Or wait for handler completion? Let's wait.
	}

	// --- Prepare and Run Handler Synchronously ---
	handlerCtx, cancel := context.WithCancel(context.Background())
	ac.storeCancelFunc(task.ID, cancel) // Store cancel func
	defer ac.removeCancelFunc(task.ID)  // Ensure cleanup on exit

	updates := make(chan A2AYieldUpdate, 20) // Buffered channel
	handlerErrChan := make(chan error, 1)
	handlerLogger := logger // Pass logger to handler

	go func() {
		defer close(handlerErrChan)
		defer close(updates)
		// Pass the task *with the new user message included in history*
		if err := ac.agentHandler(handlerCtx, task, updates, handlerLogger); err != nil {
			handlerErrChan <- err
		}
	}()

	// --- Process Updates Synchronously ---
	var lastTaskState *a2aSchema.Task = task // Start with the task state before handler call
	for update := range updates {
		var applyErr error
		lastTaskState, applyErr = ac.applyUpdateToTask(lastTaskState, update)
		if applyErr != nil {
			logger.Error("Failed to apply update to task", zap.Error(applyErr), zap.Any("update", update))
			lastTaskState.Status = createErrorStatus(applyErr) // Update status to reflect internal error
			// Since this is sync, we should save this error state and return it.
			if saveErr := ac.taskStore.Save(context.Background(), lastTaskState); saveErr != nil {
				logger.Error("Failed to save internal error task state", zap.Error(saveErr))
			}
			// We still need to wait for the handler goroutine to potentially finish/error out.
			// Drain the rest of the updates channel? Or just break and use this error state? Let's break.
			cancel() // Cancel handler context as we encountered an internal error
			break
		}
		// Optionally save intermediate states? For sync, usually only final state matters.
	}

	// --- Handle Handler Completion/Error ---
	handlerErr := <-handlerErrChan // Wait for handler goroutine to finish
	if handlerErr != nil && !errors.Is(handlerErr, context.Canceled) {
		logger.Error("Agent handler returned an error", zap.Error(handlerErr))
		// Ensure final task state reflects failure
		if lastTaskState == nil { // Should not happen if task was loaded
			lastTaskState = task
		}
		lastTaskState.Status = createErrorStatus(handlerErr)
	} else if lastTaskState == nil {
		logger.Error("Handler finished but no final task state recorded")
		task.Status = createErrorStatus(errors.New("internal error: Handler finished unexpectedly"))
		lastTaskState = task
	} else if !isTerminalState(lastTaskState.Status.State) {
		// Handler finished without error, but didn't set a terminal state. Assume completed.
		logger.Warn("Handler finished successfully but task not in terminal state. Setting to 'completed'.", zap.String("finalState", string(lastTaskState.Status.State)))
		lastTaskState.Status.State = a2aSchema.TaskStateCompleted
		lastTaskState.Status.Timestamp = time.Now()
	}

	// --- Save Final State and Return ---
	if err := ac.taskStore.Save(context.Background(), lastTaskState); err != nil {
		logger.Error("Failed to save final task state", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save task state"})
	}

	// --- Handle History Length in Response ---
	finalResponseTask := *lastTaskState // Copy final state
	if params.HistoryLength != nil && *params.HistoryLength >= 0 {
		historyLen := *params.HistoryLength
		if len(finalResponseTask.History) > historyLen {
			finalResponseTask.History = finalResponseTask.History[len(finalResponseTask.History)-historyLen:]
		}
	} else {
		finalResponseTask.History = nil // Return no history if length is not specified or negative
	}

	logger.Debug("tasks/send completed", zap.String("finalState", string(finalResponseTask.Status.State)))
	return &finalResponseTask, nil // Return the potentially history-trimmed final task object
}

// handleTaskSendSubscribe handles requests to start a task and stream updates via SSE.
func (ac *A2ACapability) handleTaskSendSubscribe(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/sendSubscribe"))

	var params a2aSchema.TaskSendParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/sendSubscribe params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()})
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/sendSubscribe request")

	// --- Load or Create Task ---
	task, err := ac.loadOrCreateTask(context.Background(), params.ID, params.SessionID, params.Metadata)
	if err != nil {
		logger.Error("Failed to load/create task", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to initialize task"})
	}

	// --- Check if Task is Active ---
	if ac.isHandlerRunning(task.ID) {
		logger.Warn("Received tasks/sendSubscribe for already active task")
		// For SSE, allow re-subscribing? Or return error? Let's return error for now.
		// Re-subscribe should likely use tasks/resubscribe.
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidRequest, Message: "Task is already processing, use tasks/resubscribe"})
	}
	if isTerminalState(task.Status.State) && params.Message.Role == "user" {
		logger.Info("Continuing completed task via sendSubscribe", zap.String("previousState", string(task.Status.State)))
		task.Status.State = a2aSchema.TaskStateSubmitted // Reset state? Or let handler decide?
	}

	// --- Add User Message to History ---
	if params.Message.Role == "user" {
		task.History = append(task.History, params.Message)
		// Save immediately so handler sees it? Yes.
		if err := ac.taskStore.Save(context.Background(), task); err != nil {
			logger.Error("Failed to save task history before starting handler", zap.Error(err))
			return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save task state"})
		}
	}

	// --- Prepare and Start Handler Asynchronously ---
	// Use the session's context or background if session context isn't available/suitable
	handlerCtx, cancel := context.WithCancel(context.Background()) // TODO: Link to request context if possible?
	ac.storeCancelFunc(task.ID, cancel)                            // Store cancel func

	updates := make(chan A2AYieldUpdate, 20) // Buffered channel
	handlerLogger := logger                  // Pass logger to handler

	// Goroutine to run the agent's logic
	go func() {
		// Ensure cleanup when handler goroutine exits
		defer ac.removeCancelFunc(task.ID)
		defer cancel()
		defer close(updates)

		// Run the actual agent logic
		handlerErr := ac.agentHandler(handlerCtx, task, updates, handlerLogger)

		// Handle Handler Completion/Error (after update loop finishes processing)
		if handlerErr != nil && !errors.Is(handlerErr, context.Canceled) {
			logger.Error("Agent handler returned an error during streaming", zap.Error(handlerErr))
			// Send final "failed" status update via the session's SSE stream
			failStatus := createErrorStatus(handlerErr)
			finalEvent := &shared.A2AStreamEvent{
				Type: "status",
				Status: &a2aSchema.TaskStatusUpdateEvent{
					ID:     task.ID,
					Status: failStatus,
					Final:  true,
				},
				Final: true, // Mark the stream event itself as final
			}
			// Send error event via session
			if sendErr := msg.Session.SendA2AStreamEvent(finalEvent); sendErr != nil {
				logger.Error("Failed to send final failed status event", zap.Error(sendErr))
			}
			// Save the final failed state
			task.Status = failStatus // Update local task copy before saving
			if saveErr := ac.taskStore.Save(context.Background(), task); saveErr != nil {
				logger.Error("Failed to save final failed task state", zap.Error(saveErr))
			}
		} else if errors.Is(handlerErr, context.Canceled) {
			logger.Info("Handler execution cancelled", zap.Error(handlerErr))
			// Final status (canceled) should have been set and saved by handleTaskCancel
		} else {
			logger.Debug("Agent handler finished processing stream normally")
			// Ensure the task is marked as completed if it wasn't explicitly failed/canceled
			// Reload task state to ensure we have the latest saved version
			finalTaskState, loadErr := ac.taskStore.Load(context.Background(), task.ID)
			if loadErr != nil {
				logger.Error("Failed to load task state after handler completion", zap.Error(loadErr))
				// Send internal error?
			} else if !isTerminalState(finalTaskState.Status.State) {
				logger.Warn("Handler finished but task not in terminal state. Sending 'completed'.")
				finalStatus := a2aSchema.TaskStatus{
					State:     a2aSchema.TaskStateCompleted,
					Timestamp: time.Now(),
					Message:   finalTaskState.Status.Message, // Keep last message?
				}
				finalEvent := &shared.A2AStreamEvent{
					Type: "status",
					Status: &a2aSchema.TaskStatusUpdateEvent{
						ID:     task.ID,
						Status: finalStatus,
						Final:  true,
					},
					Final: true,
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
	}()

	// Goroutine to process updates and send SSE events via the session
	go func() {
		var lastTaskState *a2aSchema.Task = task // Track state locally for saving
		isFinalSent := false

		for update := range updates {
			// Apply update to local task state copy
			var applyErr error
			lastTaskState, applyErr = ac.applyUpdateToTask(lastTaskState, update)
			if applyErr != nil {
				logger.Error("Failed to apply update to task during streaming", zap.Error(applyErr), zap.Any("update", update))
				// Optionally send an error event? Or rely on the handler goroutine to send final fail?
				// Let's rely on the handler goroutine's final error handling.
				continue // Skip saving/sending this specific broken update
			}

			// Save the updated task state (use handlerCtx for potential cancellation)
			if err := ac.taskStore.Save(handlerCtx, lastTaskState); err != nil {
				logger.Error("Failed to save task state during streaming", zap.Error(err))
				// If save fails, maybe stop processing? For now, log and continue sending events.
			}

			// Prepare A2AStreamEvent to send to client
			var eventToSend *shared.A2AStreamEvent
			isFinal := isTerminalState(lastTaskState.Status.State)

			if update.Status != nil {
				eventToSend = &shared.A2AStreamEvent{
					Type: "status",
					Status: &a2aSchema.TaskStatusUpdateEvent{
						ID:     task.ID,
						Status: *update.Status, // Use status directly from update
						Final:  isFinal,
					},
					Final: isFinal, // Mark stream event itself as final if state is terminal
				}
			} else if update.Artifact != nil {
				eventToSend = &shared.A2AStreamEvent{
					Type: "artifact",
					Artifact: &a2aSchema.TaskArtifactUpdateEvent{
						ID:       task.ID,
						Artifact: *update.Artifact,
					},
					Final: false, // Artifact updates don't terminate the stream
				}
			}

			// Send event via session's output channel (handles SSE formatting)
			if eventToSend != nil {
				if err := msg.Session.SendA2AStreamEvent(eventToSend); err != nil {
					logger.Error("Failed to send A2A stream event", zap.Error(err))
					// If sending fails (e.g., client disconnected), cancel the handler context.
					ac.cancelHandler(task.ID) // Use the helper method
					return                    // Stop processing updates for this task
				}
				if isFinal {
					isFinalSent = true
					logger.Debug("Sent final status event via SSE", zap.String("state", string(lastTaskState.Status.State)))
					// Don't return yet, the handler goroutine manages final cleanup.
				}
			}
		} // End update processing loop

		logger.Debug("Update processing loop finished for task", zap.String("taskID", task.ID))
		// If handler finishes normally but didn't send a terminal status, the handler goroutine will handle it.
		if !isFinalSent {
			logger.Debug("No final event was sent explicitly during update processing")
		}
	}() // End update processing goroutine

	// For tasks/sendSubscribe, the initial JSON-RPC response acknowledges the request.
	// We return the initial task state (potentially trimmed history).
	initialResponseTask := *task // Copy initial task state
	if params.HistoryLength != nil && *params.HistoryLength >= 0 {
		historyLen := *params.HistoryLength
		if len(initialResponseTask.History) > historyLen {
			initialResponseTask.History = initialResponseTask.History[len(initialResponseTask.History)-historyLen:]
		}
	} else {
		initialResponseTask.History = nil // No history requested
	}

	logger.Debug("tasks/sendSubscribe initiated, returning initial task state", zap.String("initialState", string(initialResponseTask.Status.State)))
	return &initialResponseTask, nil
}

// handleTaskGet handles requests to retrieve task status and artifacts.
func (ac *A2ACapability) handleTaskGet(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/get"))

	var params a2aSchema.TaskQueryParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/get params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()})
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/get request")

	task, err := ac.taskStore.Load(context.Background(), params.ID)
	if err != nil {
		logger.Warn("Failed to load task for get", zap.Error(err))
		var taskNotFoundErr *a2aSchema.JSONRPCError // Check for specific error type
		if errors.As(err, &taskNotFoundErr) && taskNotFoundErr.Code == a2aSchema.ErrorCodeTaskNotFound {
			return nil, err // Return the specific TaskNotFound error
		}
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to load task state"})
	}

	// Handle historyLength
	responseTask := *task // Copy task
	if params.HistoryLength != nil && *params.HistoryLength >= 0 {
		historyLen := *params.HistoryLength
		if len(responseTask.History) > historyLen {
			// Return only the last 'historyLen' messages
			responseTask.History = responseTask.History[len(responseTask.History)-historyLen:]
		}
		// If historyLen is 0, keep empty slice. If history is nil, keep nil.
	} else {
		// If historyLength is omitted or negative, return no history
		responseTask.History = nil
	}

	logger.Debug("Returning task state", zap.String("state", string(responseTask.Status.State)))
	return &responseTask, nil
}

// handleTaskCancel handles requests to cancel a task.
func (ac *A2ACapability) handleTaskCancel(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/cancel"))

	var params a2aSchema.TaskIdParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/cancel params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()})
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/cancel request")

	task, err := ac.taskStore.Load(context.Background(), params.ID)
	if err != nil {
		logger.Warn("Failed to load task for cancellation", zap.Error(err))
		var taskNotFoundErr *a2aSchema.JSONRPCError
		if errors.As(err, &taskNotFoundErr) && taskNotFoundErr.Code == a2aSchema.ErrorCodeTaskNotFound {
			return nil, err // Return specific error
		}
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to load task state"})
	}

	// Check if task is already in a terminal state
	if isTerminalState(task.Status.State) {
		logger.Warn("Task already in terminal state, cannot cancel", zap.String("state", string(task.Status.State)))
		return nil, a2aSchema.NewTaskNotCancelableError(params.ID)
	}

	// Cancel the handler's context if it's running
	cancelled := ac.cancelHandler(params.ID) // This also removes from map
	if !cancelled {
		logger.Warn("Cancel requested, but no running handler found (might have finished?)")
		// If handler finished between Load and cancelHandler call, the state might be terminal now. Reload?
		// Let's proceed to set state to canceled, assuming the user intent is cancellation.
	}

	// Update task state to canceled
	task.Status.State = a2aSchema.TaskStateCanceled
	task.Status.Timestamp = time.Now()
	cancelMsg := "Task canceled by request."
	task.Status.Message = &a2aSchema.Message{Role: "agent", Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &cancelMsg}}}

	// Save updated state
	if err := ac.taskStore.Save(context.Background(), task); err != nil {
		logger.Error("Failed to save canceled task state", zap.Error(err))
		// What to return? Internal error seems appropriate.
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save canceled task state"})
	}

	// If the task was associated with an active SSE stream, send final canceled event.
	// We need the original session that started the stream. This info isn't easily available here.
	// The SSE sending loop should detect the context cancellation originating from cancelHandler.
	// Alternatively, the capability could store sessionID with the cancel func.

	logger.Debug("Task canceled successfully")
	// Return the updated task state (without history)
	task.History = nil
	return task, nil
}

// handleTaskPushNotificationSet handles setting push notification config (Not Implemented).
func (ac *A2ACapability) handleTaskPushNotificationSet(msg *shared.Message) (interface{}, error) {
	return nil, a2aSchema.NewUnsupportedOperationError("tasks/pushNotification/set")
}

// handleTaskPushNotificationGet handles getting push notification config (Not Implemented).
func (ac *A2ACapability) handleTaskPushNotificationGet(msg *shared.Message) (interface{}, error) {
	return nil, a2aSchema.NewUnsupportedOperationError("tasks/pushNotification/get")
}

// handleTaskResubscribe handles requests to resume streaming (Not Implemented Robustly).
func (ac *A2ACapability) handleTaskResubscribe(msg *shared.Message) (interface{}, error) {
	// Basic implementation: Treat like sendSubscribe for now.
	// A real implementation would need to:
	// 1. Load the task.
	// 2. Check if it's still active (not terminal).
	// 3. If active, potentially find the *existing* running handler/update channel.
	// 4. Re-attach the *new* session's SSE stream to forward updates from that channel.
	// 5. If not active but terminal, send final state event and close stream.
	// 6. This is complex and requires careful state management.
	ac.logger.Warn("tasks/resubscribe called, treating as tasks/sendSubscribe (resumption not fully implemented)")
	// Re-use sendSubscribe logic, which will error out if handler is already running for the task ID.
	// A better approach might be to load the task and immediately send its current state + final=true if terminal.
	// If not terminal and handler running, error? Or try to hook into existing stream? Difficult.
	return ac.handleTaskSendSubscribe(msg)
}

// --- Helper Methods ---

func (ac *A2ACapability) loadOrCreateTask(ctx context.Context, taskID string, sessionID *string, metadata *map[string]interface{}) (*a2aSchema.Task, error) {
	task, err := ac.taskStore.Load(ctx, taskID)
	if err == nil {
		ac.logger.Debug("Loaded existing task", zap.String("taskID", taskID), zap.String("state", string(task.Status.State)))
		// Task exists, potentially update metadata or session ID if provided?
		// For now, just return the loaded task.
		// If the task is terminal, the calling handler might reset state or reject.
		return task, nil
	}

	// Check if error is *not* TaskNotFoundError
	var taskNotFoundErr *a2aSchema.JSONRPCError
	if !errors.As(err, &taskNotFoundErr) || taskNotFoundErr.Code != a2aSchema.ErrorCodeTaskNotFound {
		// Unexpected error loading task
		ac.logger.Error("Unexpected error loading task", zap.String("taskID", taskID), zap.Error(err))
		return nil, err
	}

	// Task not found, create a new one
	newTask := &a2aSchema.Task{
		ID:        taskID,
		SessionID: sessionID, // Use pointer directly
		Status: a2aSchema.TaskStatus{
			State:     a2aSchema.TaskStateSubmitted,
			Timestamp: time.Now(), // Use ISO string
		},
		Artifacts: []a2aSchema.Artifact{}, // Initialize slice
		History:   []a2aSchema.Message{},  // Initialize slice
		Metadata:  metadata,               // Use pointer directly
	}

	if err := ac.taskStore.Save(ctx, newTask); err != nil {
		ac.logger.Error("Failed to save newly created task", zap.String("taskID", taskID), zap.Error(err))
		return nil, fmt.Errorf("failed to save newly created task: %w", err)
	}
	ac.logger.Info("Created new A2A task", zap.String("taskID", taskID))
	return newTask, nil
}

// applyUpdateToTask modifies the task based on the yielded update from the handler.
// It returns a *new* task instance with the update applied.
func (ac *A2ACapability) applyUpdateToTask(task *a2aSchema.Task, update A2AYieldUpdate) (*a2aSchema.Task, error) {
	if task == nil {
		return nil, errors.New("cannot apply update to nil task")
	}

	// Work on a copy to avoid race conditions
	taskCopy := *task

	if update.Status != nil {
		// Replace status, ensuring timestamp is set
		taskCopy.Status = *update.Status
		if taskCopy.Status.Timestamp.IsZero() {
			taskCopy.Status.Timestamp = time.Now()
		}
		// Add agent message from status to history
		if taskCopy.Status.Message != nil && taskCopy.Status.Message.Role == "agent" {
			if taskCopy.History == nil {
				taskCopy.History = []a2aSchema.Message{}
			}
			// Avoid adding duplicate status messages if state hasn't changed? Maybe not necessary.
			taskCopy.History = append(taskCopy.History, *taskCopy.Status.Message)
		}
	} else if update.Artifact != nil {
		artifact := *update.Artifact
		if taskCopy.Artifacts == nil {
			taskCopy.Artifacts = make([]a2aSchema.Artifact, 0, 1)
		}

		found := false
		for i := range taskCopy.Artifacts {
			if taskCopy.Artifacts[i].Index == artifact.Index {
				if artifact.Append != nil && *artifact.Append {
					// Append parts
					taskCopy.Artifacts[i].Parts = append(taskCopy.Artifacts[i].Parts, artifact.Parts...)
					// Update other fields if provided
					if artifact.LastChunk != nil {
						taskCopy.Artifacts[i].LastChunk = artifact.LastChunk
					}
					if artifact.Description != nil {
						taskCopy.Artifacts[i].Description = artifact.Description
					}
					if artifact.Metadata != nil {
						taskCopy.Artifacts[i].Metadata = artifact.Metadata
					}
					if artifact.Name != nil { // Allow updating name on append?
						taskCopy.Artifacts[i].Name = artifact.Name
					}
				} else {
					// Overwrite artifact at this index
					taskCopy.Artifacts[i] = artifact
				}
				found = true
				break
			}
		}
		if !found {
			// Append new artifact if index not found
			taskCopy.Artifacts = append(taskCopy.Artifacts, artifact)
		}
		// Update task status timestamp when an artifact is added/updated
		taskCopy.Status.Timestamp = time.Now()

	} else {
		return nil, errors.New("invalid A2AYieldUpdate: missing status or artifact")
	}

	return &taskCopy, nil
}

func (ac *A2ACapability) storeCancelFunc(taskID string, cancel context.CancelFunc) {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	if existingCancel, ok := ac.runningHandlers[taskID]; ok {
		ac.logger.Warn("Replacing/cancelling existing running handler for task ID", zap.String("taskID", taskID))
		existingCancel() // Cancel the previous one
	}
	ac.runningHandlers[taskID] = cancel
	ac.logger.Debug("Stored cancel function for task", zap.String("taskID", taskID))
}

func (ac *A2ACapability) removeCancelFunc(taskID string) {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	if _, ok := ac.runningHandlers[taskID]; ok {
		delete(ac.runningHandlers, taskID)
		ac.logger.Debug("Removed cancel function for task", zap.String("taskID", taskID))
	}
}

func (ac *A2ACapability) isHandlerRunning(taskID string) bool {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	_, ok := ac.runningHandlers[taskID]
	return ok
}

func (ac *A2ACapability) cancelHandler(taskID string) bool {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	if cancel, ok := ac.runningHandlers[taskID]; ok {
		ac.logger.Info("Cancelling running handler context for task", zap.String("taskID", taskID))
		cancel()
		delete(ac.runningHandlers, taskID) // Remove after cancelling
		return true
	}
	ac.logger.Debug("No running handler found to cancel for task", zap.String("taskID", taskID))
	return false
}

// SetCapabilities adds A2A marker to server capabilities if needed (currently none defined in MCP schema).
func (ac *A2ACapability) SetCapabilities(s *mcpSchema.ServerCapabilities) {
	// MCP schema doesn't have a dedicated A2A section.
	// We could use the Experimental field if needed.
	if s.Experimental == nil {
		s.Experimental = make(map[string]json.RawMessage)
	}
	a2aMarker, _ := json.Marshal(true)
	s.Experimental["a2a"] = a2aMarker // Simple marker
	ac.logger.Debug("Marked A2A capability in ServerCapabilities (experimental)")
}

// isTerminalState checks if a task state is final.
func isTerminalState(state a2aSchema.TaskState) bool {
	switch state {
	case a2aSchema.TaskStateCompleted, a2aSchema.TaskStateFailed, a2aSchema.TaskStateCanceled:
		return true
	default:
		return false
	}
}

// Need to add SetManager to A2ACapability
func (ac *A2ACapability) SetManager(manager mcp.ISessionManager) {
	ac.manager = manager
}
