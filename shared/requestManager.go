package shared

import (
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// RequestCallback is a function that handles response messages.
// Returns true if the request can be deleted from the requests map.
type RequestCallback func(msg *Message)

// Request holds information about a sent request.
type Request struct {
	Callback  RequestCallback
	Timestamp time.Time
}

// RequestManager manages requests and their callbacks.
type RequestManager struct {
	requests map[string]Request
	mu       sync.RWMutex
	logger   *zap.Logger
}

// NewRequestManager creates a new RequestManager instance.
func NewRequestManager(logger *zap.Logger) *RequestManager {
	return &RequestManager{
		requests: make(map[string]Request),
		logger:   logger,
	}
}

// RegisterRequest registers a request with its callback for later processing.
func (rm *RequestManager) RegisterRequest(id *schema.RequestID, callback RequestCallback) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.requests[id.String()] = Request{
		Callback:  callback,
		Timestamp: time.Now(),
	}
	rm.logger.Debug("RegisterRequest", zap.String("message_id", id.String()), zap.Int("requests_len", len(rm.requests)))
}

// ProcessResponse processes a response message by invoking its callback if available.
// Returns true if a callback was found and invoked.
func (rm *RequestManager) ProcessResponse(msg *Message) bool {
	if msg.ID == nil {
		rm.logger.Error("No message ID found")
		return false
	}

	rm.mu.RLock()
	request, exists := rm.requests[msg.ID.String()]
	rm.mu.RUnlock()

	if !exists || request.Callback == nil {
		rm.logger.Error("No callback found for message", zap.String("message_id", msg.ID.String()), zap.String("session_id", msg.Session.GetID()))
		return false // Indicate callback was not found/invoked
	}

	request.Callback(msg)
	msg.Processed = true

	rm.mu.Lock()
	delete(rm.requests, msg.ID.String())
	rm.logger.Debug("callback found, called, and now delete", zap.String("message_id", msg.ID.String()), zap.Int("requests_len", len(rm.requests)))
	rm.mu.Unlock()

	return true
}
