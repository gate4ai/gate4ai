package capability

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/shared"

	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// PromptHandler is a function that processes a prompt request and returns content
// It receives the message (containing session and parameters) and returns metadata, prompt messages, and error.
type PromptHandler func(msg *shared.Message) (*schema.Meta, []schema.PromptMessage, error)

var _ shared.IServerCapability = (*PromptsCapability)(nil) // Ensure interface implementation

// PromptsCapability handles prompt management and related requests.
type PromptsCapability struct {
	logger    *zap.Logger
	manager   mcp.ISessionManager // Keep manager reference if needed for notifications
	mu        sync.RWMutex
	prompts   map[string]*Prompt                                    // Regular prompts map: name -> Prompt
	templates map[string]*Template                                  // Templates map: name -> Template
	handlers  map[string]func(*shared.Message) (interface{}, error) // Map method -> handler function
}

// Prompt represents a prompt entity (using 2025 schema).
type Prompt struct {
	schema.Prompt // Embed the V2025 Prompt definition
	Handler       PromptHandler
	LastModified  time.Time
}

// Template represents a prompt template entity (using 2025 schema).
type Template struct {
	schema.Prompt // Embed the V2025 Prompt definition (name, description, arguments)
	Handler       PromptHandler
	LastModified  time.Time
}

// NewPromptsCapability creates a new PromptsCapability.
func NewPromptsCapability(logger *zap.Logger, manager mcp.ISessionManager) *PromptsCapability {
	pc := &PromptsCapability{
		logger:    logger.Named("prompts-capability"),
		manager:   manager, // Store the manager
		prompts:   make(map[string]*Prompt),
		templates: make(map[string]*Template),
	}
	pc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"prompts/list": pc.handlePromptsList,
		"prompts/get":  pc.handlePromptsGet,
	}

	return pc
}

func (pc *PromptsCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return pc.handlers
}

func (pc *PromptsCapability) SetCapabilities(s *schema.ServerCapabilities) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	// Only set the capability if prompts or templates are registered
	if len(pc.prompts) > 0 || len(pc.templates) > 0 {
		pc.logger.Debug("Setting Prompts capability in ServerCapabilities")
		s.Prompts = &schema.Capability{
			ListChanged: true, // Indicate that list can change
		}
	}
}

// AddPrompt adds a new prompt (not a template) with the specified details.
func (pc *PromptsCapability) AddPrompt(name string, description string, handler PromptHandler) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if _, exists := pc.prompts[name]; exists {
		return fmt.Errorf("prompt '%s' already exists", name)
	}
	if _, exists := pc.templates[name]; exists {
		return fmt.Errorf("template '%s' already exists", name)
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil for prompt '%s'", name)
	}

	pc.prompts[name] = &Prompt{
		Prompt: schema.Prompt{
			Name:        name,
			Description: description,
		},
		Handler:      handler,
		LastModified: time.Now(),
	}
	pc.logger.Info("Added prompt", zap.String("name", name))
	go pc.broadcastPromptsChanged()
	return nil
}

// UpdatePrompt updates an existing prompt.
func (pc *PromptsCapability) UpdatePrompt(name string, description string, handler PromptHandler) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	prompt, exists := pc.prompts[name]
	if !exists {
		return fmt.Errorf("prompt '%s' not found", name)
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil for prompt '%s'", name)
	}
	prompt.Description = description
	prompt.Handler = handler
	prompt.LastModified = time.Now()
	pc.logger.Info("Updated prompt", zap.String("name", name))
	go pc.broadcastPromptsChanged() // Notify clients
	return nil
}

// DeletePrompt removes a prompt by name.
func (pc *PromptsCapability) DeletePrompt(name string) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if _, exists := pc.prompts[name]; !exists {
		return fmt.Errorf("prompt '%s' not found", name)
	}
	delete(pc.prompts, name)
	pc.logger.Info("Deleted prompt", zap.String("name", name))
	go pc.broadcastPromptsChanged() // Notify clients
	return nil
}

// AddTemplate adds a new prompt template.
func (pc *PromptsCapability) AddTemplate(name string, description string, arguments []schema.PromptArgument, handler PromptHandler) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if _, exists := pc.templates[name]; exists {
		return fmt.Errorf("template '%s' already exists", name)
	}
	if _, exists := pc.prompts[name]; exists {
		return fmt.Errorf("prompt '%s' already exists", name)
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil for template '%s'", name)
	}

	pc.templates[name] = &Template{
		Prompt: schema.Prompt{
			Name:        name,
			Description: description,
			Arguments:   arguments,
		},
		Handler:      handler,
		LastModified: time.Now(),
	}
	pc.logger.Info("Added prompt template", zap.String("name", name))
	go pc.broadcastPromptsChanged() // Notify clients
	return nil
}

// DeleteTemplate removes a prompt template by name.
func (pc *PromptsCapability) DeleteTemplate(name string) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if _, exists := pc.templates[name]; !exists {
		return fmt.Errorf("template '%s' not found", name)
	}
	delete(pc.templates, name)
	pc.logger.Info("Deleted prompt template", zap.String("name", name))
	go pc.broadcastPromptsChanged() // Notify clients
	return nil
}

// broadcastPromptsChanged sends notification (kept internal).
func (pc *PromptsCapability) broadcastPromptsChanged() {
	if pc.manager == nil {
		pc.logger.Error("Manager not set for broadcast")
		return
	}
	pc.manager.NotifyEligibleSessions("notifications/prompts/list_changed", nil)
	pc.logger.Debug("Broadcasted prompts list changed notification")
}

// handlePromptsList handles the "prompts/list" request.
func (pc *PromptsCapability) handlePromptsList(msg *shared.Message) (interface{}, error) {
	logger := pc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "prompts/list"))
	logger.Debug("Handling prompts list request")

	pc.mu.RLock()
	defer pc.mu.RUnlock()

	var params schema.ListPromptsRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil {
			logger.Warn("Failed to unmarshal list params", zap.Error(err))
			return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
		}
	}

	allPrompts := make([]schema.Prompt, 0, len(pc.prompts)+len(pc.templates))
	for _, prompt := range pc.prompts {
		allPrompts = append(allPrompts, prompt.Prompt)
	}
	for _, template := range pc.templates {
		allPrompts = append(allPrompts, template.Prompt)
	}

	// TODO: Implement pagination based on params.Cursor

	result := schema.ListPromptsResult{
		Prompts:         allPrompts,
		PaginatedResult: schema.PaginatedResult{NextCursor: nil},
	}
	logger.Debug("Returning prompt list", zap.Int("count", len(result.Prompts)))
	return result, nil
}

// handlePromptsGet handles the "prompts/get" request.
func (pc *PromptsCapability) handlePromptsGet(msg *shared.Message) (interface{}, error) {
	logger := pc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "prompts/get"))

	var params schema.GetPromptRequestParams
	if msg.Params == nil {
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: "Missing params"})
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal get params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
	}
	logger = logger.With(zap.String("promptName", params.Name))
	logger.Debug("Handling get prompt request")

	pc.mu.RLock()
	prompt, promptExists := pc.prompts[params.Name]
	template, templateExists := pc.templates[params.Name]
	pc.mu.RUnlock()

	var handler PromptHandler
	var description string
	if promptExists {
		handler = prompt.Handler
		description = prompt.Description
	} else if templateExists {
		handler = template.Handler
		description = template.Description
	} else {
		logger.Warn("Prompt/template not found")
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorServerError, Message: fmt.Sprintf("Prompt or template not found: %s", params.Name)})
	} // Use ServerError range

	if handler == nil {
		logger.Error("Handler is nil", zap.String("name", params.Name))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: fmt.Sprintf("Internal error: handler not available for '%s'", params.Name)})
	}

	logger.Debug("Calling handler")
	meta, messages, err := handler(msg)
	if err != nil {
		logger.Error("Prompt handler error", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorServerError, Message: fmt.Sprintf("Handler failed: %v", err)})
	} // Use ServerError range

	result := schema.GetPromptResult{
		Meta:        meta,
		Description: description,
		Messages:    messages,
	}
	logger.Debug("Successfully generated prompt content")
	return result, nil
}
