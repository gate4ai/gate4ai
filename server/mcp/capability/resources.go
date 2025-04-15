package capability

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/shared"

	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// SubscriptionOperation represents the type of subscription event.
type SubscriptionOperation int

const (
	Subscribe   SubscriptionOperation = iota // Client subscribed to a resource
	Unsubscribe                              // Client unsubscribed from a resource
)

// SubscriptionHandler is a function type for callbacks on subscription events.
type SubscriptionHandler func(session shared.ISession, operation SubscriptionOperation, uri string, count int)

// ResourceHandler is a function that processes a resource read request.
type ResourceHandler func(msg *shared.Message) (schema.Meta, []schema.ResourceContent, error)

var _ shared.IServerCapability = (*ResourcesCapability)(nil) // Ensure interface implementation

// ResourcesCapability handles resource management, reading, and subscriptions.
type ResourcesCapability struct {
	logger                *zap.Logger
	manager               *mcp.Manager
	mu                    sync.RWMutex
	resources             map[string]*Resource
	templates             map[string]*ResourceTemplate
	subscribers           map[string]map[string]bool // URI -> SessionID -> true
	subscribeOnSubscribes []SubscriptionHandler
	handlers              map[string]func(*shared.Message) (interface{}, error)
}

// Resource represents a resource entity.
type Resource struct {
	schema.Resource
	Handler      ResourceHandler
	LastModified time.Time
}

// ResourceTemplate represents a resource template entity.
type ResourceTemplate struct {
	schema.ResourceTemplate
	Handler ResourceHandler // Optional handler for templates
}

// NewResourcesCapability creates a new ResourcesCapability.
func NewResourcesCapability(manager *mcp.Manager, logger *zap.Logger) *ResourcesCapability {
	rc := &ResourcesCapability{
		manager:               manager,
		logger:                logger.Named("resources-capability"),
		resources:             make(map[string]*Resource),
		templates:             make(map[string]*ResourceTemplate),
		subscribers:           make(map[string]map[string]bool),
		subscribeOnSubscribes: make([]SubscriptionHandler, 0),
	}
	rc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"resources/list":           rc.handleResourcesList,
		"resources/read":           rc.handleResourcesRead,
		"resources/subscribe":      rc.handleResourcesSubscribe,
		"resources/unsubscribe":    rc.handleResourcesUnsubscribe,
		"resources/templates/list": rc.handleResourceTemplatesList,
	}
	return rc
}

func (rc *ResourcesCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return rc.handlers
}

func (rc *ResourcesCapability) SetCapabilities(s *schema.ServerCapabilities) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	// Only set capability if resources or templates exist, or subscription handlers are present
	if len(rc.resources) > 0 || len(rc.templates) > 0 || len(rc.subscribeOnSubscribes) > 0 {
		rc.logger.Debug("Setting Resources capability in ServerCapabilities")
		s.Resources = &schema.CapabilityWithSubscribe{
			ListChanged: true,
			Subscribe:   true, // Assume subscribe is supported if capability is present
		}
	}
}

// AddResource adds a new resource.
func (rc *ResourcesCapability) AddResource(uri string, name string, description string, mimeType string, handler ResourceHandler) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if _, exists := rc.resources[uri]; exists {
		return fmt.Errorf("resource '%s' already exists", uri)
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil for resource '%s'", uri)
	}
	rc.resources[uri] = &Resource{
		Resource: schema.Resource{
			URI:         uri,
			Name:        name,
			Description: description,
			MimeType:    mimeType,
		},
		Handler:      handler,
		LastModified: time.Now(),
	}
	rc.logger.Info("Added resource", zap.String("uri", uri))
	go rc.broadcastResourcesListChanged() // Notify clients
	return nil
}

// UpdateResource updates an existing resource.
func (rc *ResourcesCapability) UpdateResource(uri string, name string, description string, mimeType string, handler ResourceHandler) error {
	rc.mu.Lock()
	resource, exists := rc.resources[uri]
	if !exists {
		rc.mu.Unlock()
		return fmt.Errorf("resource '%s' not found", uri)
	}
	if handler == nil {
		rc.mu.Unlock()
		return fmt.Errorf("handler cannot be nil for resource '%s'", uri)
	}
	resource.Name = name
	resource.Description = description
	resource.MimeType = mimeType
	resource.Handler = handler
	resource.LastModified = time.Now()
	rc.mu.Unlock()
	rc.logger.Info("Updated resource", zap.String("uri", uri))
	go rc.NotifyResourceUpdated(uri) // Notify subscribers immediately on update
	return nil
}

// DeleteResource removes a resource.
func (rc *ResourcesCapability) DeleteResource(uri string) error {
	rc.mu.Lock()
	if _, exists := rc.resources[uri]; !exists {
		rc.mu.Unlock()
		return fmt.Errorf("resource '%s' not found", uri)
	}
	delete(rc.resources, uri)
	delete(rc.subscribers, uri) // Also remove subscribers
	rc.mu.Unlock()
	rc.logger.Info("Deleted resource", zap.String("uri", uri))
	go rc.broadcastResourcesListChanged() // Notify clients about list change
	return nil
}

// AddResourceTemplate adds a new resource template.
func (rc *ResourcesCapability) AddResourceTemplate(uriTemplate string, name string, description string, mimeType string, handler ResourceHandler) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if _, exists := rc.templates[uriTemplate]; exists {
		return fmt.Errorf("template '%s' already exists", uriTemplate)
	}
	rc.templates[uriTemplate] = &ResourceTemplate{
		ResourceTemplate: schema.ResourceTemplate{
			URITemplate: uriTemplate,
			Name:        name,
			Description: description,
			MimeType:    mimeType,
		},
		Handler: handler, // Can be nil
	}
	rc.logger.Info("Added resource template", zap.String("uriTemplate", uriTemplate))
	// No standard notification for template list changes
	return nil
}

// DeleteResourceTemplate removes a resource template.
func (rc *ResourcesCapability) DeleteResourceTemplate(uriTemplate string) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if _, exists := rc.templates[uriTemplate]; !exists {
		return fmt.Errorf("template '%s' not found", uriTemplate)
	}
	delete(rc.templates, uriTemplate)
	rc.logger.Info("Deleted resource template", zap.String("uriTemplate", uriTemplate))
	// No standard notification for template list changes
	return nil
}

// TriggerResourceUpdate marks resource as updated and notifies subscribers.
func (rc *ResourcesCapability) TriggerResourceUpdate(uri string) error {
	rc.mu.Lock()
	resource, exists := rc.resources[uri]
	if !exists {
		rc.mu.Unlock()
		return fmt.Errorf("resource '%s' not found", uri)
	}
	resource.LastModified = time.Now()
	rc.mu.Unlock()
	rc.logger.Debug("Triggering update notification for resource", zap.String("uri", uri))
	go rc.NotifyResourceUpdated(uri)
	return nil
}

// broadcastResourcesListChanged sends notification (kept internal).
func (rc *ResourcesCapability) broadcastResourcesListChanged() {
	if rc.manager == nil {
		rc.logger.Error("Manager not set for broadcast")
		return
	}
	rc.manager.NotifyEligibleSessions("notifications/resources/list_changed", nil)
	rc.logger.Debug("Broadcasted resources list changed notification")
}

// NotifyResourceUpdated sends notification to subscribers.
func (rc *ResourcesCapability) NotifyResourceUpdated(uri string) {
	if rc.manager == nil {
		rc.logger.Error("Manager not set for notification")
		return
	}
	rc.mu.RLock()
	subscribersMap, exists := rc.subscribers[uri]
	if !exists || len(subscribersMap) == 0 {
		rc.mu.RUnlock()
		return
	}
	subscriberIDs := make([]string, 0, len(subscribersMap))
	for id := range subscribersMap {
		subscriberIDs = append(subscriberIDs, id)
	}
	rc.mu.RUnlock()

	notificationParams := &schema.ResourceUpdatedNotificationParams{URI: uri}
	rc.logger.Debug("Notifying subscribers about resource update", zap.String("uri", uri), zap.Int("count", len(subscriberIDs)))

	var wg sync.WaitGroup
	for _, sessionID := range subscriberIDs {
		wg.Add(1)
		go func(sID string) {
			defer wg.Done()
			s, err := rc.manager.GetSession(sID)
			if err != nil {
				rc.logger.Warn("Failed to get session for notification, removing subscription", zap.Error(err), zap.String("uri", uri), zap.String("sessionID", sID))
				rc.removeSubscription(s, uri)
				return
			}
			s.SendNotification("notifications/resources/updated", notificationParams.AsMap())
		}(sessionID)
	}
	wg.Wait()
}

// handleResourcesList handles the "resources/list" request.
func (rc *ResourcesCapability) handleResourcesList(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/list"))
	logger.Debug("Handling resources list request")
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	var params schema.ListResourcesRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil { /*...*/
			return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
		}
	}
	// TODO: Implement pagination
	resourcesList := make([]schema.Resource, 0, len(rc.resources))
	for _, r := range rc.resources {
		resourcesList = append(resourcesList, r.Resource)
	}
	result := schema.ListResourcesResult{
		Resources:       resourcesList,
		PaginatedResult: schema.PaginatedResult{NextCursor: nil},
	}
	logger.Debug("Returning resource list", zap.Int("count", len(result.Resources)))
	return result, nil
}

// handleResourcesRead handles the "resources/read" request.
func (rc *ResourcesCapability) handleResourcesRead(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/read"))
	var params schema.ReadResourceRequestParams
	if msg.Params == nil {
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: "Missing params"})
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil { /*...*/
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
	}
	logger = logger.With(zap.String("uri", params.URI))
	logger.Debug("Handling resource read request")
	rc.mu.RLock()
	resource, exists := rc.resources[params.URI]
	rc.mu.RUnlock()
	if !exists {
		logger.Warn("Resource not found")
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorServerError, Message: fmt.Sprintf("Resource not found: %s", params.URI)})
	} // Use ServerError range
	if resource.Handler == nil {
		logger.Error("Handler is nil")
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: fmt.Sprintf("Internal error: no handler for resource %s", params.URI)})
	}
	logger.Debug("Calling resource handler")
	meta, contents, err := resource.Handler(msg)
	if err != nil {
		logger.Error("Resource handler error", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorServerError, Message: fmt.Sprintf("Handler failed: %v", err)})
	} // Use ServerError range
	result := schema.ReadResourceResult{Meta: meta, Contents: contents}
	logger.Debug("Successfully read resource", zap.Int("contentParts", len(result.Contents)))
	return result, nil
}

// handleResourceTemplatesList handles the "resources/templates/list" request.
func (rc *ResourcesCapability) handleResourceTemplatesList(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/templates/list"))
	logger.Debug("Handling resource templates list request")
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	var params schema.ListResourceTemplatesRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil { /*...*/
			return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
		}
	}
	// TODO: Implement pagination
	templatesList := make([]schema.ResourceTemplate, 0, len(rc.templates))
	for _, t := range rc.templates {
		templatesList = append(templatesList, t.ResourceTemplate)
	}
	result := schema.ListResourceTemplatesResult{
		ResourceTemplates: templatesList,
		PaginatedResult:   schema.PaginatedResult{NextCursor: nil},
	}
	logger.Debug("Returning resource templates list", zap.Int("count", len(result.ResourceTemplates)))
	return result, nil
}

// AddSubscriptionHandler adds a handler for subscription events.
func (rc *ResourcesCapability) AddSubscriptionHandler(handler SubscriptionHandler) {
	if handler == nil {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.subscribeOnSubscribes = append(rc.subscribeOnSubscribes, handler)
	rc.logger.Debug("Added subscription handler")
}

// RemoveSubscriptionHandler removes a specific handler.
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
		rc.logger.Warn("Attempted to remove unregistered subscription handler")
	}
}

// notifySubscriptionHandlers notifies registered handlers.
func (rc *ResourcesCapability) notifySubscriptionHandlers(session shared.ISession, operation SubscriptionOperation, uri string, count int) {
	rc.mu.RLock()
	handlers := make([]SubscriptionHandler, len(rc.subscribeOnSubscribes))
	copy(handlers, rc.subscribeOnSubscribes)
	rc.mu.RUnlock()
	if len(handlers) == 0 {
		return
	}
	opStr := "subscribe"
	if operation == Unsubscribe {
		opStr = "unsubscribe"
	}
	rc.logger.Debug("Notifying subscription handlers", zap.String("operation", opStr), zap.String("uri", uri), zap.String("sessionID", session.GetID()), zap.Int("currentCount", count))
	for _, handler := range handlers {
		go func(h SubscriptionHandler) {
			defer func() {
				if r := recover(); r != nil {
					rc.logger.Error("Panic in subscription handler", zap.Any("panic", r), zap.String("uri", uri))
				}
			}()
			h(session, operation, uri, count)
		}(handler)
	}
}

// removeSubscription handles the logic for removing a subscription internally and notifying handlers.
func (rc *ResourcesCapability) removeSubscription(session shared.ISession, uri string) {
	rc.mu.Lock()
	var currentCount int
	wasSubscribed := false
	if subscribersMap, exists := rc.subscribers[uri]; exists {
		if _, subscribed := subscribersMap[session.GetID()]; subscribed {
			wasSubscribed = true
			delete(subscribersMap, session.GetID())
			currentCount = len(subscribersMap)
			if currentCount == 0 {
				delete(rc.subscribers, uri)
			}
		}
	}
	rc.mu.Unlock()

	if wasSubscribed {
		rc.logger.Info("Resource subscription removed", zap.String("uri", uri), zap.String("sessionID", session.GetID()), zap.Int("remainingCount", currentCount))
		go rc.notifySubscriptionHandlers(session, Unsubscribe, uri, currentCount)
	}
}

// handleResourcesSubscribe handles the "resources/subscribe" request.
func (rc *ResourcesCapability) handleResourcesSubscribe(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/subscribe"))
	var params schema.SubscribeRequestParams
	if msg.Params == nil {
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: "Missing params"})
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil { /*...*/
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
	}
	logger = logger.With(zap.String("uri", params.URI))
	logger.Debug("Handling resource subscribe request")

	rc.mu.Lock()
	// Check if resource exists before subscribing
	if _, exists := rc.resources[params.URI]; !exists {
		rc.mu.Unlock()
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorServerError, Message: fmt.Sprintf("Cannot subscribe to unknown resource: %s", params.URI)})
	} // Use ServerError range
	if rc.subscribers[params.URI] == nil {
		rc.subscribers[params.URI] = make(map[string]bool)
	}
	isNewSubscription := !rc.subscribers[params.URI][msg.Session.GetID()]
	rc.subscribers[params.URI][msg.Session.GetID()] = true
	currentCount := len(rc.subscribers[params.URI])
	rc.mu.Unlock()

	if isNewSubscription {
		logger.Info("Resource subscription added", zap.Int("currentCount", currentCount))
		go rc.notifySubscriptionHandlers(msg.Session, Subscribe, params.URI, currentCount)
	} else {
		logger.Debug("Client re-subscribed", zap.Int("currentCount", currentCount))
	}
	return map[string]interface{}{}, nil // Success
}

// handleResourcesUnsubscribe handles the "resources/unsubscribe" request.
func (rc *ResourcesCapability) handleResourcesUnsubscribe(msg *shared.Message) (interface{}, error) {
	logger := rc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "resources/unsubscribe"))
	var params schema.UnsubscribeRequestParams
	if msg.Params == nil {
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: "Missing params"})
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil { /*...*/
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
	}
	logger = logger.With(zap.String("uri", params.URI))
	logger.Debug("Handling resource unsubscribe request")

	rc.removeSubscription(msg.Session, params.URI) // Use helper

	return map[string]interface{}{}, nil // Success
}

// GetSubscribedResources returns URIs with active subscribers.
func (rc *ResourcesCapability) GetSubscribedResources() []string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	uris := make([]string, 0, len(rc.subscribers))
	for uri, subs := range rc.subscribers {
		if len(subs) > 0 {
			uris = append(uris, uri)
		}
	}
	return uris
}
