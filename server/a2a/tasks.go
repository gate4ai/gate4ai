package a2a

import (
	"context"
	"sync"

	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
)

// TaskStore defines the interface for storing and retrieving A2A task states.
type TaskStore interface {
	Save(ctx context.Context, task *a2aSchema.Task) error
	Load(ctx context.Context, taskID string) (*a2aSchema.Task, error)
	Delete(ctx context.Context, taskID string) error
}

// InMemoryTaskStore implements TaskStore using an in-memory map.
type InMemoryTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*a2aSchema.Task
}

// NewInMemoryTaskStore creates a new InMemoryTaskStore.
func NewInMemoryTaskStore() *InMemoryTaskStore {
	return &InMemoryTaskStore{
		tasks: make(map[string]*a2aSchema.Task),
	}
}

// Save stores a copy of the task in the map.
func (s *InMemoryTaskStore) Save(ctx context.Context, task *a2aSchema.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Create a copy to store, avoid holding reference to caller's object
	taskCopy := *task
	// Ensure slices are copied if needed, though shallow copy is often sufficient here
	// if task.Artifacts != nil {
	// 	taskCopy.Artifacts = append([]a2aSchema.Artifact{}, task.Artifacts...)
	// }
	// if task.History != nil {
	// 	taskCopy.History = append([]a2aSchema.Message{}, task.History...)
	// }
	s.tasks[task.ID] = &taskCopy
	return nil
}

// Load retrieves a copy of the task from the map.
func (s *InMemoryTaskStore) Load(ctx context.Context, taskID string) (*a2aSchema.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, exists := s.tasks[taskID]
	if !exists {
		return nil, a2aSchema.NewTaskNotFoundError(taskID)
	}
	// Return a copy to prevent mutation by caller
	taskCopy := *task
	// Ensure slices are copied if needed
	// if task.Artifacts != nil {
	// 	taskCopy.Artifacts = append([]a2aSchema.Artifact{}, task.Artifacts...)
	// }
	// if task.History != nil {
	// 	taskCopy.History = append([]a2aSchema.Message{}, task.History...)
	// }
	return &taskCopy, nil
}

// Delete removes a task from the map.
func (s *InMemoryTaskStore) Delete(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[taskID]; !exists {
		return a2aSchema.NewTaskNotFoundError(taskID)
	}
	delete(s.tasks, taskID)
	return nil
}
