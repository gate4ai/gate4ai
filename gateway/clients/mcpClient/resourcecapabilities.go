package mcpClient

import (
	"context"

	"github.com/gate4ai/gate4ai/gateway/clients/mcpClient/capability"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
)

// Resources returns the resources capability instance
func (s *Session) Resources() *capability.ResourcesCapability {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	return s.ResourcesCapability
}

// ResourceTemplates returns the resource templates capability instance
func (s *Session) ResourceTemplates() *capability.ResourceTemplatesCapability {
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	return s.ResourceTemplatesCapability
}

// Helper methods to simplify capability usage

// SubscribeResource sends a request to subscribe to updates for a given resource URI.
func (s *Session) SubscribeResource(ctx context.Context, uri string) error {
	return s.Resources().SubscribeResource(ctx, uri)
}

// UnsubscribeResource sends a request to unsubscribe from updates for a given resource URI.
func (s *Session) UnsubscribeResource(ctx context.Context, uri string) error {
	return s.Resources().UnsubscribeResource(ctx, uri)
}

// SubscribeOnResourceUpdated registers a callback function to be invoked when a resource update notification is received.
func (s *Session) SubscribeOnResourceUpdated(f capability.ResourceUpdatedFunc) {
	s.Resources().SubscribeOnResourceUpdated(f)
}

// UnsubscribeFromResourceUpdated removes a previously registered callback function.
func (s *Session) UnsubscribeFromResourceUpdated(f capability.ResourceUpdatedFunc) {
	s.Resources().UnsubscribeFromResourceUpdated(f)
}

// ListResourceTemplates retrieves all available resource templates.
func (s *Session) ListResourceTemplates(ctx context.Context) ([]schema.ResourceTemplate, error) {
	return s.ResourceTemplates().ListResourceTemplates(ctx)
}

// GetResourceTemplate retrieves a specific resource template by ID.
func (s *Session) GetResourceTemplate(ctx context.Context, id string) (*schema.ResourceTemplate, error) {
	return s.ResourceTemplates().GetResourceTemplate(ctx, id)
}

// CreateResourceFromTemplate creates a new resource from a template.
func (s *Session) CreateResourceFromTemplate(ctx context.Context, templateID string, params interface{}) (*schema.Resource, error) {
	return s.ResourceTemplates().CreateResourceFromTemplate(ctx, templateID, params)
}
