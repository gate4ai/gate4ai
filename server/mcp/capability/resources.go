package capability

import (
	"encoding/json"
	"fmt"
	"reflect" // Import reflect for RemoveSubscriptionHandler
	"sync"
	"time"

	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/shared"

	// Use 2025 schema
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// SubscriptionOperation represents the type of subscription event.
type SubscriptionOperation int

const (
	Subscribe   SubscriptionOperation = iota // Client subscribed to a resource
	Unsubscribe                              // Client unsubscribed from a resource
)

// SubscriptionHandler is a function type for callbacks on subscription events.
// Receives the session, operation type, URI, and current subscriber count for that URI.
type SubscriptionHandler func(session shared.ISession, operation SubscriptionOperation, uri string, count int)

// ResourceHandler is a function that processes a resource read request (using 2025 schema types).
// It receives the message (containing session and URI) and returns metadata, resource contents, and error.
type ResourceHandler func(msg *shared.Message) (schema.Meta, []schema.ResourceContent, error)

var _ shared.IServerCapability = (*ResourcesCapability)(nil)

// ResourcesCapability handles resource management, reading, and subscriptions.
type ResourcesCapability struct {
	logger                *zap.Logger
	manager               *mcp.Manager // To get sessions for notifications
	mu                    sync.RWMutex
	resources             map[string]*Resource                                  // Map URI -> Resource
	templates             map[string]*ResourceTemplate                          // Map URI Template -> ResourceTemplate
	subscribers           map[string]map[string]bool                            // Map resource URI -> Set of session IDs {sessionID: true}
	subscribeOnSubscribes []SubscriptionHandler                                 // List of handlers to call on subscribe/unsubscribe events
	handlers              map[string]func(*shared.Message) (interface{}, error) // Map method -> handler function
}

// Resource represents a resource entity (using 2025 schema).
type Resource struct {
	schema.Resource // Embed the V2025 Resource definition
	Handler         ResourceHandler
	LastModified    time.Time
}

// ResourceTemplate represents a resource template entity (using 2025 schema).
type ResourceTemplate struct {
	schema.ResourceTemplate                 // Embed the V2025 ResourceTemplate definition
	Handler                 ResourceHandler // Handler might be used for validation or dynamic generation? Optional.
}

// NewResourcesCapability creates a new ResourcesCapability.
func NewResourcesCapability(manager *mcp.Manager, logger *zap.Logger) *ResourcesCapability {
	rc := &ResourcesCapability{
		manager:               manager,
		logger:                logger,
		resources:             make(map[string]*Resource),
		templates:             make(map[string]*ResourceTemplate),
		subscribers:           make(map[string]map[string]bool),
		subscribeOnSubscribes: make([]SubscriptionHandler, 0),
	}
	// Initialize handlers
	rc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"resources/list":           rc.handleResourcesList,
		"resources/read":           rc.handleResourcesRead,
		"resources/subscribe":      rc.handleResourcesSubscribe,
		"resources/unsubscribe":    rc.handleResourcesUnsubscribe,
		"resources/templates/list": rc.handleResourceTemplatesList,
	}

	return rc
}

// GetHandlers returns a map of method names to handler functions
// This satisfies the shared.ICapability interface
func (rc *ResourcesCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return rc.handlers
}

// SetCapabilities sets the server capabilities for this capability
func (rc *ResourcesCapability) SetCapabilities(s *schema.ServerCapabilities) {
	rc.logger.Debug("SetCapabilities called on ResourcesCapability")
	s.Resources = &schema.CapabilityWithSubscribe{
		ListChanged: true,
		Subscribe:   true,
	}
}

// AddResource adds a new resource with the specified details (using 2025 schema).
func (rc *ResourcesCapability) AddResource(uri string, name string, description string, mimeType string, handler ResourceHandler) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if _, exists := rc.resources[uri]; exists {
		return fmt.Errorf("resource with URI '%s' already exists", uri)
	}

	rc.resources[uri] = &Resource{
		Resource: schema.Resource{
			URI:         uri,
			Name:        name,
			Description: description,
			MimeType:    mimeType,
			// Annotations can be added if needed
		},
		Handler:      handler,
		LastModified: time.Now(),
	}

	rc.logger.Info("Added resource", zap.String("uri", uri))
	go rc.broadcastResourcesListChanged() // Notify clients
	return nil
}

// UpdateResource updates an existing resource and notifies subscribers.
func (rc *ResourcesCapability) UpdateResource(uri string, name string, description string, mimeType string, handler ResourceHandler) error {
	rc.mu.Lock()

	resource, exists := rc.resources[uri]
	if !exists {
		rc.mu.Unlock()
		// Option: Create if not exists? For now, return error.
		return fmt.Errorf("resource with URI '%s' does not exist", uri)
		// Alternatively, call AddResource:
		// rc.mu.Unlock()
		// return rc.AddResource(uri, name, description, mimeType, handler)
	}

	// Update fields
	resource.Name = name
	resource.Description = description
	resource.MimeType = mimeType
	resource.Handler = handler
	resource.LastModified = time.Now()
	// Annotations can be updated too if needed

	rc.mu.Unlock() // Unlock before notifying

	rc.logger.Info("Updated resource", zap.String("uri", uri))
	// Notify subscribers about the update
	go rc.NotifyResourceUpdated(uri)
	// Note: Resource list itself didn't change (no add/delete), so no broadcastResourcesListChanged needed.

	return nil
}

// DeleteResource removes a resource by URI.
func (rc *ResourcesCapability) DeleteResource(uri string) error {
	rc.mu.Lock()

	if _, exists := rc.resources[uri]; !exists {
		rc.mu.Unlock()
		return fmt.Errorf("resource with URI '%s' does not exist", uri)
	}

	delete(rc.resources, uri)

	// Also remove any subscriptions for the deleted resource
	delete(rc.subscribers, uri)

	rc.mu.Unlock() // Unlock before notifying

	rc.logger.Info("Deleted resource", zap.String("uri", uri))
	go rc.broadcastResourcesListChanged() // Notify clients about list change
	return nil
}

// AddResourceTemplate adds a new resource template (using 2025 schema).
func (rc *ResourcesCapability) AddResourceTemplate(uriTemplate string, name string, description string, mimeType string, handler ResourceHandler) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if _, exists := rc.templates[uriTemplate]; exists {
		return fmt.Errorf("resource template with URI template '%s' already exists", uriTemplate)
	}

	rc.templates[uriTemplate] = &ResourceTemplate{
		ResourceTemplate: schema.ResourceTemplate{
			URITemplate: uriTemplate,
			Name:        name,
			Description: description,
			MimeType:    mimeType,
			// Annotations can be added if needed
		},
		Handler: handler, // Handler might be optional for templates
	}

	rc.logger.Info("Added resource template", zap.String("uriTemplate", uriTemplate))
	// Note: V2025 schema doesn't define a notification for template list changes.
	// If needed, a custom notification could be implemented.
	return nil
}

// DeleteResourceTemplate removes a resource template by URI template.
func (rc *ResourcesCapability) DeleteResourceTemplate(uriTemplate string) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if _, exists := rc.templates[uriTemplate]; !exists {
		return fmt.Errorf("resource template with URI template '%s' does not exist", uriTemplate)
	}

	delete(rc.templates, uriTemplate)
	rc.logger.Info("Deleted resource template", zap.String("uriTemplate", uriTemplate))
	// No standard notification for template list changes.
	return nil
}

// TriggerResourceUpdate explicitly marks a resource as updated (updates timestamp) and notifies subscribers.
// Useful if the resource content changes externally without calling UpdateResource.
func (rc *ResourcesCapability) TriggerResourceUpdate(uri string) error {
	rc.mu.Lock()
	resource, exists := rc.resources[uri]
	if !exists {
		rc.mu.Unlock()
		return fmt.Errorf("cannot trigger update for non-existent resource URI '%s'", uri)
	}
	resource.LastModified = time.Now()
	rc.mu.Unlock() // Unlock before notifying

	rc.logger.Debug("Triggering update notification for resource", zap.String("uri", uri))
	go rc.NotifyResourceUpdated(uri)
	return nil
}

// broadcastResourcesListChanged sends a "notifications/resources/list_changed" notification.
func (rc *ResourcesCapability) broadcastResourcesListChanged() {
	if rc.manager == nil {
		rc.logger.Error("Cannot broadcast resource list changed: manager not set")
		return
	}
	rc.manager.NotifyEligibleSessions("notifications/resources/list_changed", nil)
	rc.logger.Debug("Broadcasted resources list changed notification")
}

// NotifyResourceUpdated sends a "notifications/resources/updated" notification to subscribers of a specific URI.
func (rc *ResourcesCapability) NotifyResourceUpdated(uri string) {
	if rc.manager == nil {
		rc.logger.Error("Cannot send resource update notification: manager not set")
		return
	}

	rc.mu.RLock()
	// Get the set of session IDs subscribed to this URI
	subscribersMap, exists := rc.subscribers[uri]
	if !exists || len(subscribersMap) == 0 {
		rc.mu.RUnlock()
		rc.logger.Debug("No active subscribers for resource update", zap.String("uri", uri))
		return // No subscribers for this resource
	}

	// Create a snapshot of subscriber IDs to notify
	subscriberIDs := make([]string, 0, len(subscribersMap))
	for sessionID := range subscribersMap {
		subscriberIDs = append(subscriberIDs, sessionID)
	}
	rc.mu.RUnlock()

	notificationParams := &schema.ResourceUpdatedNotificationParams{
		URI: uri,
	}

	rc.logger.Debug("Notifying subscribers about resource update", zap.String("uri", uri), zap.Int("count", len(subscriberIDs)))

	// Send notification to each subscribed session
	var wg sync.WaitGroup
	for _, sessionID := range subscriberIDs {
		wg.Add(1)
		go func(sID string) {
			defer wg.Done()
			s, err := rc.manager.GetSession(sID)
			if err != nil {
				rc.logger.Warn("Failed to get session for notification, removing subscription", zap.Error(err), zap.String("uri", uri), zap.String("sessionID", sID))
				// Clean up stale subscription
				rc.mu.Lock()
				if subs, ok := rc.subscribers[uri]; ok {
					delete(subs, sID)
					if len(subs) == 0 {
						delete(rc.subscribers, uri) // Clean up map entry if no subscribers left
					}
				}
				rc.mu.Unlock()
				// Notify handlers about the implicit unsubscribe due to session error
				// Need to reconstruct a dummy session or handle nil session in handlers
				// For now, just log and remove.
				return
			}
			// Send the notification
			s.SendNotification("notifications/resources/updated", notificationParams.AsMap())
			rc.logger.Debug("Sent resource update notification", zap.String("uri", uri), zap.String("sessionID", sID))
		}(sessionID)
	}
	wg.Wait() // Wait for all notifications to be sent
}

// handleResourcesList handles the "resources/list" request.
func (rc *ResourcesCapability) handleResourcesList(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/list"))
	logger.Debug("Handling resources list request")

	rc.mu.RLock()
	defer rc.mu.RUnlock()

	// Parse pagination parameters (V2025)
	var params schema.ListResourcesRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil {
			logger.Warn("Failed to unmarshal pagination params", zap.Error(err))
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}
	// TODO: Implement pagination based on params.Cursor

	// Collect all resources
	resourcesList := make([]schema.Resource, 0, len(rc.resources))
	for _, resource := range rc.resources {
		resourcesList = append(resourcesList, resource.Resource) // Add embedded V2025 Resource
	}

	// Sort?

	// Apply pagination

	// Construct V2025 result
	result := schema.ListResourcesResult{
		Resources: resourcesList,
		PaginatedResult: schema.PaginatedResult{
			NextCursor: nil, // Set if paginating
		},
	}

	logger.Debug("Returning resource list", zap.Int("count", len(result.Resources)))
	return result, nil
}

// handleResourcesRead handles the "resources/read" request.
func (rc *ResourcesCapability) handleResourcesRead(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/read"))

	// Parse parameters (V2025)
	var params schema.ReadResourceRequestParams
	if msg.Params == nil {
		logger.Warn("Missing parameters in resources/read request")
		return nil, fmt.Errorf("invalid request: missing params")
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal resources/read params", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	logger = logger.With(zap.String("uri", params.URI))
	logger.Debug("Handling resource read request")

	rc.mu.RLock()
	resource, exists := rc.resources[params.URI]
	rc.mu.RUnlock()

	if !exists {
		logger.Warn("Resource not found")
		return nil, fmt.Errorf("resource not found: %s", params.URI)
	}

	if resource.Handler == nil {
		logger.Error("Resource found but handler is nil")
		return nil, fmt.Errorf("internal error: no handler available for resource %s", params.URI)
	}

	// Call the resource handler
	logger.Debug("Calling resource handler")
	meta, contents, err := resource.Handler(msg)
	if err != nil {
		logger.Error("Resource handler returned an error", zap.Error(err))
		return nil, fmt.Errorf("handler for resource '%s' failed: %w", params.URI, err)
	}

	// Construct V2025 result
	result := schema.ReadResourceResult{
		Meta:     meta,
		Contents: contents, // Handler should return []schema.ResourceContent (V2025)
	}

	logger.Debug("Successfully read resource", zap.Int("contentParts", len(result.Contents)))
	return result, nil
}

// handleResourceTemplatesList handles the "resources/templates/list" request.
func (rc *ResourcesCapability) handleResourceTemplatesList(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/templates/list"))
	logger.Debug("Handling resource templates list request")

	rc.mu.RLock()
	defer rc.mu.RUnlock()

	// Parse pagination parameters (V2025)
	var params schema.ListResourceTemplatesRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil {
			logger.Warn("Failed to unmarshal pagination params", zap.Error(err))
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}
	// TODO: Implement pagination based on params.Cursor

	// Collect all templates
	templatesList := make([]schema.ResourceTemplate, 0, len(rc.templates))
	for _, template := range rc.templates {
		templatesList = append(templatesList, template.ResourceTemplate) // Add embedded V2025 ResourceTemplate
	}

	// Sort?
	// Apply pagination

	// Construct V2025 result
	result := schema.ListResourceTemplatesResult{
		ResourceTemplates: templatesList,
		PaginatedResult: schema.PaginatedResult{
			NextCursor: nil, // Set if paginating
		},
	}

	logger.Debug("Returning resource templates list", zap.Int("count", len(result.ResourceTemplates)))
	return result, nil
}

// --- Subscription Handling ---

// AddSubscriptionHandler adds a handler to be notified about subscription changes.
func (rc *ResourcesCapability) AddSubscriptionHandler(handler SubscriptionHandler) {
	if handler == nil {
		return // Ignore nil handlers
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.subscribeOnSubscribes = append(rc.subscribeOnSubscribes, handler)
	rc.logger.Debug("Added subscription handler")
}

// RemoveSubscriptionHandler removes a specific handler from the notification list.
func (rc *ResourcesCapability) RemoveSubscriptionHandler(handler SubscriptionHandler) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	targetPtr := reflect.ValueOf(handler).Pointer()
	found := false
	newHandlers := rc.subscribeOnSubscribes[:0]
	for _, h := range rc.subscribeOnSubscribes {
		if reflect.ValueOf(h).Pointer() != targetPtr {
			newHandlers = append(newHandlers, h)
		} else {
			found = true
		}
	}
	rc.subscribeOnSubscribes = newHandlers

	if found {
		rc.logger.Debug("Removed subscription handler")
	} else {
		rc.logger.Warn("Attempted to remove a subscription handler that was not registered")
	}
}

// notifySubscriptionHandlers notifies all registered handlers about a subscription event.
func (rc *ResourcesCapability) notifySubscriptionHandlers(session shared.ISession, operation SubscriptionOperation, uri string, count int) {
	rc.mu.RLock()
	// Create a snapshot of handlers under lock
	handlers := make([]SubscriptionHandler, len(rc.subscribeOnSubscribes))
	copy(handlers, rc.subscribeOnSubscribes)
	rc.mu.RUnlock() // Release lock before calling handlers

	if len(handlers) == 0 {
		return // No handlers registered
	}

	opStr := "subscribe"
	if operation == Unsubscribe {
		opStr = "unsubscribe"
	}
	rc.logger.Debug("Notifying subscription handlers",
		zap.String("operation", opStr),
		zap.String("uri", uri),
		zap.String("sessionID", session.GetID()),
		zap.Int("currentCount", count),
		zap.Int("handlerCount", len(handlers)))

	for _, handler := range handlers {
		// Call handlers in separate goroutines to avoid blocking? Depends on handler behavior.
		// For now, call sequentially. Consider goroutines if handlers might block.
		go func(h SubscriptionHandler) { // Run in goroutine for safety
			defer func() {
				if r := recover(); r != nil {
					rc.logger.Error("Panic recovered in subscription handler", zap.Any("panic", r), zap.String("uri", uri))
				}
			}()
			h(session, operation, uri, count)
		}(handler)
	}
}

// handleResourcesSubscribe handles the "resources/subscribe" request.
func (rc *ResourcesCapability) handleResourcesSubscribe(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/subscribe"))

	// Parse parameters (V2025)
	var params schema.SubscribeRequestParams
	if msg.Params == nil {
		logger.Warn("Missing parameters in subscribe request")
		return nil, fmt.Errorf("invalid request: missing params")
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal subscribe params", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	logger = logger.With(zap.String("uri", params.URI))
	logger.Debug("Handling resource subscribe request")

	rc.mu.Lock()

	// Check if resource exists (optional, maybe allow subscribing to non-existent resources?)
	if _, exists := rc.resources[params.URI]; !exists {
		// Check templates too? For now, only allow subscribing to existing concrete resources.
		rc.mu.Unlock()
		logger.Warn("Attempt to subscribe to unknown resource")
		return nil, fmt.Errorf("cannot subscribe to unknown resource: %s", params.URI)
	}

	// Add session ID to the subscribers list for this URI
	if rc.subscribers[params.URI] == nil {
		rc.subscribers[params.URI] = make(map[string]bool)
	}
	isNewSubscription := !rc.subscribers[params.URI][msg.Session.GetID()]
	rc.subscribers[params.URI][msg.Session.GetID()] = true
	currentCount := len(rc.subscribers[params.URI])

	rc.mu.Unlock() // Unlock before notifying handlers

	if isNewSubscription {
		logger.Info("Resource subscription added", zap.Int("currentCount", currentCount))
		// Notify handlers about the new subscription
		go rc.notifySubscriptionHandlers(msg.Session, Subscribe, params.URI, currentCount)
	} else {
		logger.Debug("Client re-subscribed to resource", zap.Int("currentCount", currentCount))
	}

	// Return simple success response (V2025 doesn't specify response content, empty object is safe)
	return map[string]interface{}{}, nil
}

// handleResourcesUnsubscribe handles the "resources/unsubscribe" request.
func (rc *ResourcesCapability) handleResourcesUnsubscribe(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/unsubscribe"))

	// Parse parameters (V2025)
	var params schema.UnsubscribeRequestParams
	if msg.Params == nil {
		logger.Warn("Missing parameters in unsubscribe request")
		return nil, fmt.Errorf("invalid request: missing params")
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal unsubscribe params", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	logger = logger.With(zap.String("uri", params.URI))
	logger.Debug("Handling resource unsubscribe request")

	rc.mu.Lock()

	var currentCount int
	wasSubscribed := false
	if subscribersMap, exists := rc.subscribers[params.URI]; exists {
		if _, subscribed := subscribersMap[msg.Session.GetID()]; subscribed {
			wasSubscribed = true
			delete(subscribersMap, msg.Session.GetID())
			currentCount = len(subscribersMap)
			// Clean up map entry if no subscribers left
			if currentCount == 0 {
				delete(rc.subscribers, params.URI)
			}
		}
	}

	rc.mu.Unlock() // Unlock before notifying handlers

	if wasSubscribed {
		logger.Info("Resource subscription removed", zap.Int("remainingCount", currentCount))
		// Notify handlers about the unsubscription
		go rc.notifySubscriptionHandlers(msg.Session, Unsubscribe, params.URI, currentCount)
	} else {
		logger.Debug("Client unsubscribed from resource it wasn't subscribed to")
	}

	// Return simple success response
	return map[string]interface{}{}, nil
}

// GetSubscribedResources returns a list of URIs that have at least one subscriber.
// Note: This function signature doesn't match the usage in startExample.go (msg *shared.Message).
// Keeping the simple signature for now.
func (rc *ResourcesCapability) GetSubscribedResources() []string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	uris := make([]string, 0, len(rc.subscribers))
	for uri, subscribers := range rc.subscribers {
		if len(subscribers) > 0 {
			uris = append(uris, uri)
		}
	}
	return uris
}
