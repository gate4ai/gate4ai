package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/mcp/server/mcp" // Needed for transport constants
	"github.com/gate4ai/mcp/shared"
	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema"
	mcpSchema "github.com/gate4ai/mcp/shared/mcp/2025/schema"

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
type A2AHandler func(ctx context.Context, task *a2aSchema.Task, updates chan<- A2AYieldUpdate) error

// --- Task Storage Interface ---

// TaskStore defines the interface for storing and retrieving A2A task states.
type TaskStore interface {
	Save(ctx context.Context, task *a2aSchema.Task) error
	Load(ctx context.Context, taskID string) (*a2aSchema.Task, error)
	// Add Delete method if needed
}

// --- In-Memory Task Store Implementation ---

type InMemoryTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*a2aSchema.Task
}

func NewInMemoryTaskStore() *InMemoryTaskStore {
	return &InMemoryTaskStore{
		tasks: make(map[string]*a2aSchema.Task),
	}
}

func (s *InMemoryTaskStore) Save(ctx context.Context, task *a2aSchema.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Create a copy to store, avoid holding reference to caller's object
	taskCopy := *task
	s.tasks[task.ID] = &taskCopy
	return nil
}

func (s *InMemoryTaskStore) Load(ctx context.Context, taskID string) (*a2aSchema.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, exists := s.tasks[taskID]
	if !exists {
		return nil, a2aSchema.NewTaskNotFoundError(taskID)
	}
	// Return a copy to prevent mutation by caller
	taskCopy := *task
	return &taskCopy, nil
}

// --- A2ACapability ---

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

func NewA2ACapability(
	logger *zap.Logger,
	manager mcp.ISessionManager,
	store TaskStore,
	handler A2AHandler,
) *A2ACapability {
	ac := &A2ACapability{
		logger:          logger.Named("a2a-capability"),
		manager:         manager,
		taskStore:       store,
		agentHandler:    handler,
		runningHandlers: make(map[string]context.CancelFunc),
	}
	ac.handlers = map[string]func(*shared.Message) (interface{}, error){
		"tasks/send":          ac.handleTaskSend,
		"tasks/sendSubscribe": ac.handleTaskSendSubscribe,
		"tasks/get":           ac.handleTaskGet,
		"tasks/cancel":        ac.handleTaskCancel,
		// TODO: Add handlers for tasks/pushNotification/*, tasks/resubscribe
	}
	return ac
}

func (ac *A2ACapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return ac.handlers
}

// --- A2A Method Handlers ---

func (ac *A2ACapability) handleTaskSend(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/send"))

	var params a2aSchema.TaskSendParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/send params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()})
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/send request")

	task, err := ac.loadOrCreateTask(context.Background(), params.ID, params.SessionID, params.Metadata)
	if err != nil {
		logger.Error("Failed to load/create task", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to initialize task"})
	}

	// Add user message to history (if implementing history)
	// task.History = append(task.History, params.Message) // Simplified

	// Prepare context for the handler
	handlerCtx, cancel := context.WithCancel(context.Background()) // Create cancellable context
	defer cancel()                                                 // Ensure cancellation on exit

	// Store cancellation function
	ac.storeCancelFunc(task.ID, cancel)
	defer ac.removeCancelFunc(task.ID) // Clean up on exit

	// Channel for handler updates (buffered to avoid blocking handler)
	updates := make(chan A2AYieldUpdate, 10) // TODO: Make buffer size configurable?

	// Start handler in a goroutine
	handlerErrChan := make(chan error, 1)
	go func() {
		defer close(handlerErrChan)
		defer close(updates) // Close updates channel when handler goroutine finishes
		if err := ac.agentHandler(handlerCtx, task, updates); err != nil {
			handlerErrChan <- err
		}
	}()

	// Process updates synchronously for tasks/send
	var lastTaskState *a2aSchema.Task
	for update := range updates {
		// Apply update to task state
		var applyErr error
		task, applyErr = ac.applyUpdateToTask(task, update)
		if applyErr != nil {
			logger.Error("Failed to apply update to task", zap.Error(applyErr), zap.Any("update", update))
			// Continue processing other updates? Or fail task? Let's fail task.
			task.Status.State = a2aSchema.TaskStateFailed
			errMsg := fmt.Sprintf("Internal error applying update: %v", applyErr)
			task.Status.Message = &a2aSchema.Message{Role: "agent", Parts: []a2aSchema.Part{{
				Type: shared.PointerTo("text"),
				Text: shared.PointerTo(errMsg),
			}}}
			task.Status.Timestamp = time.Now()
			break
		}
		lastTaskState = task // Keep track of the latest state
	}

	// Check for handler error
	if handlerErr := <-handlerErrChan; handlerErr != nil {
		logger.Error("Agent handler returned an error", zap.Error(handlerErr))
		// Ensure task state reflects failure
		if lastTaskState != nil {
			task = lastTaskState
		}
		task.Status.State = a2aSchema.TaskStateFailed
		task.Status.Message = &a2aSchema.Message{Role: "agent", Parts: []a2aSchema.Part{{
			Type: shared.PointerTo("text"),
			Text: shared.PointerTo(handlerErr.Error())}}}
		task.Status.Timestamp = time.Now()
	} else if lastTaskState == nil {
		// Should not happen if handler ran correctly, but handle defensively
		logger.Error("Handler finished but no final task state recorded")
		task.Status.State = a2aSchema.TaskStateFailed
		task.Status.Message = &a2aSchema.Message{Role: "agent", Parts: []a2aSchema.Part{{
			Type: shared.PointerTo("text"),
			Text: shared.PointerTo("Internal error: Handler finished unexpectedly")}}}
		task.Status.Timestamp = time.Now()
	} else {
		// Handler finished successfully, use the last known state
		task = lastTaskState
	}

	// Save the final task state
	if err := ac.taskStore.Save(context.Background(), task); err != nil {
		logger.Error("Failed to save final task state", zap.Error(err))
		// Return internal error, but maybe log the actual task state?
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save task state"})
	}

	logger.Debug("tasks/send completed", zap.String("finalState", string(task.Status.State)))
	return task, nil // Return the final task object
}

func (ac *A2ACapability) handleTaskSendSubscribe(msg *shared.Message) (interface{}, error) {
	logger := ac.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tasks/sendSubscribe"))

	var params a2aSchema.TaskSendParams
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tasks/sendSubscribe params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: err.Error()})
	}
	logger = logger.With(zap.String("taskID", params.ID))
	logger.Debug("Handling tasks/sendSubscribe request")

	task, err := ac.loadOrCreateTask(context.Background(), params.ID, params.SessionID, params.Metadata)
	if err != nil {
		logger.Error("Failed to load/create task", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to initialize task"})
	}

	// --- Signal Transport to Start SSE ---
	// We can't directly manipulate the HTTP response here.
	// We'll rely on the transport detecting this method + Accept header.
	// We need to start the handler and push updates to the session's output.

	// Prepare context for the handler
	// Use the session's context if possible, otherwise background
	baseCtx := context.Background()                   // TODO: Find a way to get request context if possible
	handlerCtx, cancel := context.WithCancel(baseCtx) // Create cancellable context

	// Store cancellation function
	ac.storeCancelFunc(task.ID, cancel)
	// No defer remove here, cleanup happens when stream closes or task ends

	// Channel for handler updates
	updates := make(chan A2AYieldUpdate, 10)

	// Start handler in a goroutine
	go func() {
		// Ensure context cancellation and cleanup function removal on exit
		defer ac.removeCancelFunc(task.ID)
		defer cancel()       // Cancel context if handler exits normally or panics
		defer close(updates) // Close updates channel when handler goroutine finishes

		// Run the actual agent logic
		handlerErr := ac.agentHandler(handlerCtx, task, updates)

		// --- Handle Handler Completion/Error (after loop below finishes) ---
		if handlerErr != nil {
			logger.Error("Agent handler returned an error during streaming", zap.Error(handlerErr))
			// Send a final "failed" status update via the session
			failStatus := a2aSchema.TaskStatus{
				State:     a2aSchema.TaskStateFailed,
				Message:   &a2aSchema.Message{Role: "agent", Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: shared.PointerTo(handlerErr.Error())}}},
				Timestamp: time.Now(),
			}
			finalEvent := &shared.A2AStreamEvent{
				Type: "status",
				Status: &a2aSchema.TaskStatusUpdateEvent{
					ID:     task.ID,
					Status: failStatus,
					Final:  true,
				},
				Final: true,
			}
			if sendErr := msg.Session.SendA2AStreamEvent(finalEvent); sendErr != nil {
				logger.Error("Failed to send final failed status event", zap.Error(sendErr))
			}
			// Also save the failed state
			task.Status = failStatus
			if saveErr := ac.taskStore.Save(context.Background(), task); saveErr != nil {
				logger.Error("Failed to save final failed task state", zap.Error(saveErr))
			}
		} else {
			logger.Debug("Agent handler finished processing stream")
			// The final "completed" status should have been sent via the updates channel by the handler.
			// If not, we might need to send a final event here based on the last known state.
			// Need to load the task again to be sure of final state saved by the update loop.
			finalTask, loadErr := ac.taskStore.Load(context.Background(), task.ID)
			if loadErr == nil && !isTerminalState(finalTask.Status.State) {
				logger.Warn("Handler finished but task not in terminal state. Sending 'completed'.")
				finalStatus := a2aSchema.TaskStatus{
					State:     a2aSchema.TaskStateCompleted,
					Message:   finalTask.Status.Message, // Keep last message? Or set default?
					Timestamp: time.Now(),
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
				finalTask.Status = finalStatus
				if saveErr := ac.taskStore.Save(context.Background(), finalTask); saveErr != nil {
					logger.Error("Failed to save final completed task state", zap.Error(saveErr))
				}
			}
		}
	}()

	// Goroutine to process updates and send SSE events via the session
	go func() {
		var lastSavedTask *a2aSchema.Task = task // Keep track of task state locally
		isFinalSent := false

		for update := range updates {
			// Apply update to local task state
			var applyErr error
			lastSavedTask, applyErr = ac.applyUpdateToTask(lastSavedTask, update)
			if applyErr != nil {
				logger.Error("Failed to apply update to task during streaming", zap.Error(applyErr), zap.Any("update", update))
				// Send a final error event? Or just log? Let's log and try to send final fail later.
				continue
			}

			// Save the updated task state to the store
			if err := ac.taskStore.Save(handlerCtx, lastSavedTask); err != nil { // Use handlerCtx for saving
				logger.Error("Failed to save task state during streaming", zap.Error(err))
				// If save fails, should we stop? Maybe log and continue sending events?
			}

			// Prepare A2AStreamEvent
			var eventToSend *shared.A2AStreamEvent
			isFinal := isTerminalState(lastSavedTask.Status.State)

			if update.Status != nil {
				eventToSend = &shared.A2AStreamEvent{
					Type: "status",
					Status: &a2aSchema.TaskStatusUpdateEvent{
						ID:     task.ID,
						Status: *update.Status, // Use the status from the update
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
					Final: false, // Artifact updates usually aren't final task state
				}
			}

			// Send event via session's output channel
			if eventToSend != nil {
				if err := msg.Session.SendA2AStreamEvent(eventToSend); err != nil {
					logger.Error("Failed to send A2A stream event", zap.Error(err))
					// If sending fails, maybe the client disconnected. Cancel the handler context.
					ac.cancelHandler(task.ID)
					return // Stop processing updates for this task
				}
				if isFinal {
					isFinalSent = true
					logger.Debug("Sent final status event via SSE", zap.String("state", string(lastSavedTask.Status.State)))
					// Don't return yet, handler goroutine needs to finish
				}
			}
		}

		// This goroutine finishes when the 'updates' channel is closed by the handler goroutine.
		logger.Debug("Update processing loop finished for task", zap.String("taskID", task.ID))
		// Final state handling is done in the handler goroutine's defer block.
		if !isFinalSent {
			logger.Debug("No final event was sent explicitly by handler completion")
			// Final check/sending logic happens in the handler goroutine exit now.
		}

	}()

	// For tasks/sendSubscribe, the initial response is just acknowledging the request.
	// The actual results come via the SSE stream initiated by the transport.
	// The MCP spec isn't explicit, but JSON-RPC usually requires *some* response
	// for non-notification requests. We return the *initial* task state.
	logger.Debug("tasks/sendSubscribe initiated, returning initial task state", zap.String("initialState", string(task.Status.State)))
	return task, nil
}

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
		logger.Warn("Failed to load task", zap.Error(err))
		// Check if it's TaskNotFoundError specifically
		var taskNotFoundErr *a2aSchema.TaskNotFoundError
		if errors.As(err, &taskNotFoundErr) {
			return nil, a2aSchema.NewTaskNotFoundError(params.ID)
		}
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to load task"})
	}

	// TODO: Handle historyLength if needed
	// task.History = potentially_trim_history(task.History, params.HistoryLength)

	logger.Debug("Returning task state", zap.String("state", string(task.Status.State)))
	return task, nil
}

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
		var taskNotFoundErr *a2aSchema.TaskNotFoundError
		if errors.As(err, &taskNotFoundErr) {
			return nil, a2aSchema.NewTaskNotFoundError(params.ID)
		}
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to load task"})
	}

	// Check if task is already in a terminal state
	if isTerminalState(task.Status.State) {
		logger.Warn("Task already in terminal state, cannot cancel", zap.String("state", string(task.Status.State)))
		return nil, a2aSchema.NewTaskNotCancelableError(params.ID)
	}

	// Cancel the handler's context if it's running
	cancelled := ac.cancelHandler(params.ID)
	if !cancelled {
		logger.Warn("Cancel requested, but no running handler found for task ID (might have finished?)")
		// Proceed to update state anyway? Or return error? Let's update state.
	}

	// Update task state to canceled
	task.Status.State = a2aSchema.TaskStateCanceled
	task.Status.Timestamp = time.Now()
	task.Status.Message = &a2aSchema.Message{Role: "agent", Parts: []a2aSchema.Part{{
		Type: shared.PointerTo("text"),
		Text: shared.PointerTo("Task canceled by request."),
	}}}

	// Save updated state
	if err := ac.taskStore.Save(context.Background(), task); err != nil {
		logger.Error("Failed to save canceled task state", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to save canceled task state"})
	}

	logger.Debug("Task canceled successfully")
	return task, nil // Return the updated task state
}

// --- Helper Methods ---

func (ac *A2ACapability) loadOrCreateTask(ctx context.Context, taskID string, sessionID *string, metadata *map[string]interface{}) (*a2aSchema.Task, error) {
	task, err := ac.taskStore.Load(ctx, taskID)
	if err == nil {
		// Task exists, potentially update metadata or session ID?
		// For now, just return the loaded task. Add user message to history if needed.
		return task, nil
	}

	// Check if error is *not* TaskNotFoundError
	var taskNotFoundErr *a2aSchema.TaskNotFoundError
	if !errors.As(err, &taskNotFoundErr) {
		// Unexpected error loading task
		return nil, err
	}

	// Task not found, create a new one
	newTask := &a2aSchema.Task{
		ID:        taskID,
		SessionID: sessionID,
		Status: a2aSchema.TaskStatus{
			State:     a2aSchema.TaskStateSubmitted, // Start as submitted
			Timestamp: time.Now(),
		},
		Artifacts: []a2aSchema.Artifact{},
		History:   []a2aSchema.Message{}, // Initialize history
		Metadata:  metadata,
	}

	// Save the newly created task
	if err := ac.taskStore.Save(ctx, newTask); err != nil {
		return nil, fmt.Errorf("failed to save newly created task: %w", err)
	}
	ac.logger.Info("Created new A2A task", zap.String("taskID", taskID))
	return newTask, nil
}

// applyUpdateToTask modifies the task based on the yielded update.
func (ac *A2ACapability) applyUpdateToTask(task *a2aSchema.Task, update A2AYieldUpdate) (*a2aSchema.Task, error) {
	if task == nil {
		return nil, fmt.Errorf("cannot apply update to nil task")
	}
	// Work on a copy to avoid race conditions if the original task is accessed elsewhere
	taskCopy := *task
	taskCopy.Status = task.Status // Shallow copy status initially

	if update.Status != nil {
		taskCopy.Status = *update.Status // Replace status entirely
		if taskCopy.Status.Timestamp.IsZero() {
			taskCopy.Status.Timestamp = time.Now() // Ensure timestamp is set
		}
		// Optionally add agent message from status to history
		if taskCopy.Status.Message != nil && taskCopy.Status.Message.Role == "agent" {
			// Make sure History is initialized
			if taskCopy.History == nil {
				taskCopy.History = []a2aSchema.Message{}
			}
			taskCopy.History = append(taskCopy.History, *taskCopy.Status.Message)
		}
	} else if update.Artifact != nil {
		// Make sure Artifacts slice is initialized
		if taskCopy.Artifacts == nil {
			taskCopy.Artifacts = []a2aSchema.Artifact{}
		}

		artifact := *update.Artifact
		// Handle artifact appending/updating based on index
		found := false
		for i := range taskCopy.Artifacts {
			if taskCopy.Artifacts[i].Index == artifact.Index {
				if artifact.Append != nil && *artifact.Append {
					// Append parts (handle potential part merging logic if needed)
					taskCopy.Artifacts[i].Parts = append(taskCopy.Artifacts[i].Parts, artifact.Parts...)
					// Update other fields if provided
					if artifact.LastChunk != nil {
						taskCopy.Artifacts[i].LastChunk = artifact.LastChunk
					}
					if artifact.Description != nil {
						taskCopy.Artifacts[i].Description = artifact.Description
					}
					if artifact.Metadata != nil {
						taskCopy.Artifacts[i].Metadata = artifact.Metadata // Overwrite metadata? Or merge?
					}
				} else {
					// Overwrite existing artifact at this index
					taskCopy.Artifacts[i] = artifact
				}
				found = true
				break
			}
		}
		if !found {
			// Artifact with this index not found, append new one
			taskCopy.Artifacts = append(taskCopy.Artifacts, artifact)
		}
	} else {
		return nil, fmt.Errorf("invalid A2AYieldUpdate: neither status nor artifact provided")
	}

	return &taskCopy, nil
}

func (ac *A2ACapability) storeCancelFunc(taskID string, cancel context.CancelFunc) {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	// Cancel previous handler for the same task ID if it exists
	if existingCancel, ok := ac.runningHandlers[taskID]; ok {
		ac.logger.Warn("Replacing existing running handler for task ID", zap.String("taskID", taskID))
		existingCancel()
	}
	ac.runningHandlers[taskID] = cancel
}

func (ac *A2ACapability) removeCancelFunc(taskID string) {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	delete(ac.runningHandlers, taskID)
}

func (ac *A2ACapability) cancelHandler(taskID string) bool {
	ac.runningHandlersMu.Lock()
	defer ac.runningHandlersMu.Unlock()
	if cancel, ok := ac.runningHandlers[taskID]; ok {
		ac.logger.Info("Cancelling running handler for task", zap.String("taskID", taskID))
		cancel()
		delete(ac.runningHandlers, taskID) // Remove after cancelling
		return true
	}
	return false
}

func (ac *A2ACapability) SetCapabilities(s *mcpSchema.ServerCapabilities) {
	panic("not implemented")
	//TODO: A2A - need redising for ServerCapabilities
}

// Need to add SetManager to A2ACapability
func (ac *A2ACapability) SetManager(manager mcp.ISessionManager) {
	ac.manager = manager
}

func isTerminalState(state a2aSchema.TaskState) bool {
	switch state {
	case a2aSchema.TaskStateCompleted, a2aSchema.TaskStateFailed, a2aSchema.TaskStateCanceled:
		return true
	default:
		return false
	}
}
