package capability

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"

	"github.com/gate4ai/gate4ai/shared"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// ResourceUpdatedFunc defines the callback function type for handling resource update notifications.
type ResourceUpdatedFunc func(msg *shared.Message)

var _ shared.IClientCapability = (*ResourcesCapability)(nil)

// ResourcesCapability handles resources functionality for the client.
type ResourcesCapability struct {
	logger                     *zap.Logger
	mu                         sync.RWMutex
	resourceUpdatedSubscribers []ResourceUpdatedFunc
	handlers                   map[string]func(*shared.Message) (interface{}, error)
	session                    shared.ISession // Reference to the parent session
}

// NewResourcesCapability creates a new ResourcesCapability.
func NewResourcesCapability(logger *zap.Logger, session shared.ISession) *ResourcesCapability {
	rc := &ResourcesCapability{
		logger:                     logger,
		resourceUpdatedSubscribers: []ResourceUpdatedFunc{},
		session:                    session,
	}
	rc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"notifications/resources/updated": rc.handleResourceUpdated,
	}

	return rc
}

// GetHandlers returns the map of method handlers for this capability.
func (rc *ResourcesCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return rc.handlers
}

// SetCapabilities implements the IClientCapability interface.
func (rc *ResourcesCapability) SetCapabilities(s *schema.ClientCapabilities) {
	// Since ClientCapabilities doesn't have a Resources field directly in 2024/2025 schema,
	// we'll use the Experimental field to indicate resource capabilities
	if s.Experimental == nil {
		s.Experimental = make(map[string]map[string]json.RawMessage)
	}

	// Initialize resources section if needed
	if _, exists := s.Experimental["resources"]; !exists {
		s.Experimental["resources"] = make(map[string]json.RawMessage)
	}

	// Add subscribe capability
	subscribeValue, _ := json.Marshal(true)
	s.Experimental["resources"]["subscribe"] = json.RawMessage(subscribeValue)
}

// SubscribeOnResourceUpdated registers a callback function to be invoked when a resource update notification is received.
func (rc *ResourcesCapability) SubscribeOnResourceUpdated(f ResourceUpdatedFunc) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.resourceUpdatedSubscribers = append(rc.resourceUpdatedSubscribers, f)
	rc.logger.Debug("Added resource updated subscriber", zap.Int("totalSubscribers", len(rc.resourceUpdatedSubscribers)))
}

// UnsubscribeFromResourceUpdated removes a previously registered callback function.
func (rc *ResourcesCapability) UnsubscribeFromResourceUpdated(f ResourceUpdatedFunc) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Use reflect.ValueOf().Pointer() for reliable function comparison
	targetPtr := reflect.ValueOf(f).Pointer()
	found := false
	for i := len(rc.resourceUpdatedSubscribers) - 1; i >= 0; i-- {
		if reflect.ValueOf(rc.resourceUpdatedSubscribers[i]).Pointer() == targetPtr {
			// Remove the element by swapping with the last element and slicing
			rc.resourceUpdatedSubscribers[i] = rc.resourceUpdatedSubscribers[len(rc.resourceUpdatedSubscribers)-1]
			rc.resourceUpdatedSubscribers = rc.resourceUpdatedSubscribers[:len(rc.resourceUpdatedSubscribers)-1]
			found = true
			// Continue checking in case the same function was added multiple times (though unlikely)
		}
	}

	if found {
		rc.logger.Debug("Removed resource updated subscriber", zap.Int("totalSubscribers", len(rc.resourceUpdatedSubscribers)))
	} else {
		rc.logger.Warn("Attempted to remove a resource updated subscriber that was not found")
	}
}

// SubscribeResource sends a request to subscribe to updates for a given resource URI.
func (rc *ResourcesCapability) SubscribeResource(ctx context.Context, uri string) error {
	logger := rc.logger.With(zap.String("operation", "SubscribeResource"), zap.String("uri", uri))

	if uri == "" {
		err := errors.New("resource URI cannot be empty")
		logger.Error("Invalid request", zap.Error(err))
		return err
	}

	logger.Debug("Sending resources/subscribe request")
	params := &schema.SubscribeRequestParams{
		URI: uri,
	}

	go func() {
		msg := <-rc.session.SendRequestSync("resources/subscribe", params)
		if msg.Error != nil {
			logger.Error("Error in subscribe response", zap.Error(msg.Error))
		}
	}()
	return nil
}

// UnsubscribeResource sends a request to unsubscribe from updates for a given resource URI.
func (rc *ResourcesCapability) UnsubscribeResource(ctx context.Context, uri string) error {
	logger := rc.logger.With(zap.String("operation", "UnsubscribeResource"), zap.String("uri", uri))

	if uri == "" {
		err := errors.New("resource URI cannot be empty")
		logger.Error("Invalid request", zap.Error(err))
		return err
	}

	logger.Debug("Sending resources/unsubscribe request")
	params := &schema.UnsubscribeRequestParams{
		URI: uri,
	}
	go func() {
		msg := <-rc.session.SendRequestSync("resources/unsubscribe", params)
		if msg.Error != nil {
			logger.Error("Error in unsubscribe response", zap.Error(msg.Error))
		}
	}()
	return nil
}

// handleResourceUpdated handles incoming "notifications/resources/updated" messages.
func (rc *ResourcesCapability) handleResourceUpdated(msg *shared.Message) (interface{}, error) {
	// Use logger with context from the message if available
	logger := rc.logger.With(zap.String("method", *msg.Method))
	if msg.ID != nil {
		logger = logger.With(zap.String("msgID", msg.ID.String()))
	}
	logger.Debug("Processing resource updated notification")

	// Basic validation
	if msg.Error != nil {
		logger.Error("Resource updated notification contains error", zap.Error(msg.Error))
		return nil, msg.Error
	}

	if msg.Params == nil {
		errMsg := "Resource updated notification params are nil"
		logger.Error(errMsg)
		return nil, errors.New(errMsg)
	}

	// Log the raw parameters for debugging
	logger.Debug("Raw resource update params", zap.ByteString("params", *msg.Params))

	// Minimal parsing to check URI (optional, subscribers might do full parsing)
	var params schema.ResourceUpdatedNotificationParams // Use 2025 type
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal resource updated notification params", zap.Error(err))
		// Proceed to notify subscribers anyway, they might handle raw message
	} else {
		logger.Debug("Parsed resource update URI", zap.String("uri", params.URI))
	}

	// Get subscribers safely
	rc.mu.RLock()
	// Copy slice to avoid holding lock during callbacks
	subscribersCopy := make([]ResourceUpdatedFunc, len(rc.resourceUpdatedSubscribers))
	copy(subscribersCopy, rc.resourceUpdatedSubscribers)
	subscriberCount := len(subscribersCopy)
	rc.mu.RUnlock()

	// Mark message as processed
	msg.Processed = true

	if subscriberCount > 0 {
		logger.Debug("Notifying subscribers about resource update", zap.Int("count", subscriberCount))
		for _, subscriber := range subscribersCopy {
			// Call subscriber in a goroutine to prevent blocking
			go func(cb ResourceUpdatedFunc, m *shared.Message) {
				defer func() {
					if r := recover(); r != nil {
						rc.logger.Error("Panic recovered in resource update subscriber", zap.Any("panic", r))
					}
				}()
				cb(m)
			}(subscriber, msg) // Pass the original message
		}
	} else {
		logger.Debug("No subscribers registered for resource updates")
	}

	// Return nil because notifications should not have responses
	return nil, nil
}
