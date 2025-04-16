package capability

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gate4ai/mcp/shared"
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// SamplingFunc defines the callback function type for handling sampling requests.
type SamplingFunc func(params schema.CreateMessageRequestParams) (*schema.CreateMessageResult, error)

// SamplingCapability handles sampling functionality for the client.
type SamplingCapability struct {
	logger             *zap.Logger
	mu                 sync.RWMutex
	samplingSubscriber SamplingFunc
	handlers           map[string]func(*shared.Message) (interface{}, error)
}

// NewSamplingCapability creates a new SamplingCapability.
func NewSamplingCapability(logger *zap.Logger) *SamplingCapability {
	sc := &SamplingCapability{
		logger: logger,
	}
	sc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"sampling/createMessage": sc.handleSamplingCreateMessage,
	}

	return sc
}

// GetHandlers returns the map of method handlers for this capability.
func (sc *SamplingCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return sc.handlers
}

// SetCapabilities implements the IClientCapability interface.
func (sc *SamplingCapability) SetCapabilities(s *schema.ClientCapabilities) {
	s.Sampling = &struct{}{}
}

// SubscribeOnSampling registers a callback function to handle incoming "sampling/createMessage" requests.
// Only one subscriber can be active at a time. Subsequent calls overwrite the previous subscriber.
func (sc *SamplingCapability) SubscribeOnSampling(f SamplingFunc) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.samplingSubscriber = f
	sc.logger.Info("Sampling subscriber registered")
}

// UnsubscribeFromSampling removes the currently registered sampling callback function.
func (sc *SamplingCapability) UnsubscribeFromSampling() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.samplingSubscriber != nil {
		sc.samplingSubscriber = nil
		sc.logger.Info("Sampling subscriber unregistered")
	} else {
		sc.logger.Debug("No sampling subscriber was registered, nothing to unregister")
	}
}

// handleSamplingCreateMessage handles the "sampling/createMessage" request from the server.
func (sc *SamplingCapability) handleSamplingCreateMessage(msg *shared.Message) (interface{}, error) {
	// Use logger with context from the message if available
	logger := sc.logger.With(zap.String("method", *msg.Method))
	if msg.ID != nil {
		logger = logger.With(zap.String("reqID", msg.ID.String()))
	} else {
		logger.Error("Received sampling/createMessage notification (no ID), cannot process")
		return nil, errors.New("cannot process sampling/createMessage without request ID")
	}
	logger.Debug("Processing sampling/createMessage request")

	if msg.Params == nil {
		logger.Error("Sampling message params are nil")
		return nil, fmt.Errorf("invalid request: missing params")
	}

	var params schema.CreateMessageRequestParams
	err := json.Unmarshal(*msg.Params, &params)
	if err != nil {
		logger.Error("Failed to unmarshal CreateMessageRequestParams", zap.Error(err))
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// Get the subscriber safely
	sc.mu.RLock()
	subscriber := sc.samplingSubscriber
	sc.mu.RUnlock()

	if subscriber == nil {
		logger.Warn("No sampling subscriber registered, cannot process request")
		return nil, errors.New("sampling not supported by client")
	}

	// Call the subscriber function
	logger.Debug("Calling sampling subscriber")
	result, err := subscriber(params) // Pass V2025 params

	// Handle the result from the subscriber
	if err != nil {
		logger.Error("Sampling subscriber returned an error", zap.Error(err))
		return nil, fmt.Errorf("sampling handler error: %w", err)
	}

	if result == nil {
		// This is unexpected if error was nil
		logger.Error("Sampling subscriber returned nil result and nil error")
		return nil, errors.New("internal sampling handler error: nil result")
	}

	// Mark the original request message as processed
	msg.Processed = true

	// Return the successful result
	logger.Debug("Returning sampling result")
	return result, nil
}
