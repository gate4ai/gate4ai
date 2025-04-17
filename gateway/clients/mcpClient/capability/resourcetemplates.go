package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gate4ai/gate4ai/shared"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// ResourceTemplatesCapability handles resource templates functionality for the client.
type ResourceTemplatesCapability struct {
	logger   *zap.Logger
	mu       sync.RWMutex
	handlers map[string]func(*shared.Message) (interface{}, error)
	session  shared.ISession // Reference to the parent session
}

// NewResourceTemplatesCapability creates a new ResourceTemplatesCapability.
func NewResourceTemplatesCapability(logger *zap.Logger, session shared.ISession) *ResourceTemplatesCapability {
	rtc := &ResourceTemplatesCapability{
		logger:  logger,
		session: session,
	}
	rtc.handlers = map[string]func(*shared.Message) (interface{}, error){
		// Currently no specific notification handlers for resource templates
		// Will be expanded in the future if needed
	}

	return rtc
}

// GetHandlers returns the map of method handlers for this capability.
func (rtc *ResourceTemplatesCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return rtc.handlers
}

// SetCapabilities implements the IClientCapability interface.
func (rtc *ResourceTemplatesCapability) SetCapabilities(s *schema.ClientCapabilities) {
	// Use the Experimental field to indicate resource templates capabilities
	if s.Experimental == nil {
		s.Experimental = make(map[string]map[string]json.RawMessage)
	}

	// Initialize resourceTemplates section if needed
	if _, exists := s.Experimental["resourceTemplates"]; !exists {
		s.Experimental["resourceTemplates"] = make(map[string]json.RawMessage)
	}

	// Add support capability
	supportValue, _ := json.Marshal(true)
	s.Experimental["resourceTemplates"]["supported"] = json.RawMessage(supportValue)
}

// ListResourceTemplates retrieves all available resource templates.
func (rtc *ResourceTemplatesCapability) ListResourceTemplates(ctx context.Context) ([]schema.ResourceTemplate, error) {
	logger := rtc.logger.With(zap.String("operation", "ListResourceTemplates"))
	logger.Debug("Sending resources/templates/list request")

	msg := <-rtc.session.SendRequestSync("resources/templates/list", nil)
	if msg.Error != nil {
		logger.Error("Error in list response", zap.Error(msg.Error))
		return nil, msg.Error
	}

	if msg.Result == nil {
		err := errors.New("received empty result in list response")
		logger.Error("Invalid response", zap.Error(err))
		return nil, err
	}

	// Define a local struct to match the expected response
	var result struct {
		Templates []schema.ResourceTemplate `json:"templates"`
	}

	if err := json.Unmarshal(*msg.Result, &result); err != nil {
		logger.Error("Failed to unmarshal result", zap.Error(err))
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.Templates, nil
}

// GetResourceTemplate retrieves a specific resource template by ID.
func (rtc *ResourceTemplatesCapability) GetResourceTemplate(ctx context.Context, id string) (*schema.ResourceTemplate, error) {
	logger := rtc.logger.With(zap.String("operation", "GetResourceTemplate"), zap.String("templateID", id))

	if id == "" {
		err := errors.New("template ID cannot be empty")
		logger.Error("Invalid request", zap.Error(err))
		return nil, err
	}

	logger.Debug("Sending resources/templates/get request")
	params := &struct {
		ID string `json:"id"`
	}{
		ID: id,
	}

	msg := <-rtc.session.SendRequestSync("resources/templates/get", params)
	if msg.Error != nil {
		logger.Error("Error in get response", zap.Error(msg.Error))
		return nil, msg.Error
	}

	if msg.Result == nil {
		err := errors.New("received empty result in get response")
		logger.Error("Invalid response", zap.Error(err))
		return nil, err
	}

	// Define a local struct to match the expected response
	var result struct {
		Template *schema.ResourceTemplate `json:"template"`
	}

	if err := json.Unmarshal(*msg.Result, &result); err != nil {
		logger.Error("Failed to unmarshal result", zap.Error(err))
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.Template, nil
}

// CreateResourceFromTemplate creates a new resource from a template.
func (rtc *ResourceTemplatesCapability) CreateResourceFromTemplate(ctx context.Context, templateID string, params interface{}) (*schema.Resource, error) {
	logger := rtc.logger.With(
		zap.String("operation", "CreateResourceFromTemplate"),
		zap.String("templateID", templateID),
	)

	if templateID == "" {
		err := errors.New("template ID cannot be empty")
		logger.Error("Invalid request", zap.Error(err))
		return nil, err
	}

	logger.Debug("Sending resources/create request")

	// Create request parameters
	requestParams := struct {
		TemplateID string      `json:"templateId"`
		Params     interface{} `json:"params,omitempty"`
	}{
		TemplateID: templateID,
		Params:     params,
	}

	msg := <-rtc.session.SendRequestSync("resources/create", requestParams)
	if msg.Error != nil {
		logger.Error("Error in create response", zap.Error(msg.Error))
		return nil, msg.Error
	}

	if msg.Result == nil {
		err := errors.New("received empty result in create response")
		logger.Error("Invalid response", zap.Error(err))
		return nil, err
	}

	// Define a local struct to match the expected response
	var result struct {
		Resource *schema.Resource `json:"resource"`
	}

	if err := json.Unmarshal(*msg.Result, &result); err != nil {
		logger.Error("Failed to unmarshal result", zap.Error(err))
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.Resource, nil
}

// Make sure ResourceTemplatesCapability implements the required interfaces
var (
	_ shared.ICapability       = (*ResourceTemplatesCapability)(nil)
	_ shared.IClientCapability = (*ResourceTemplatesCapability)(nil)
)
