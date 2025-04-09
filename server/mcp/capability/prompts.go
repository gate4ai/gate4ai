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

// PromptsCapability handles prompt management and related requests.
type PromptsCapability struct {
	logger    *zap.Logger
	manager   *mcp.Manager
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
// Renamed from PromptTemplate for clarity.
type Template struct {
	schema.Prompt // Embed the V2025 Prompt definition (name, description, arguments)
	Handler       PromptHandler
	LastModified  time.Time
}

// NewPromptsCapability creates a new PromptsCapability.
func NewPromptsCapability(logger *zap.Logger, manager *mcp.Manager) *PromptsCapability {
	pc := &PromptsCapability{
		logger:    logger,
		manager:   manager,
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
	s.Prompts = &schema.Capability{}
}

// AddPrompt adds a new prompt (not a template) with the specified details.
func (pc *PromptsCapability) AddPrompt(name string, description string, handler PromptHandler) error {
	// V2025 prompts don't have arguments defined directly on them, they are discovered by the client.
	// Arguments are primarily for templates. If a non-template prompt needs args, handle in handler.
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if _, exists := pc.prompts[name]; exists {
		return fmt.Errorf("prompt with name '%s' already exists", name)
	}
	// Check if name conflicts with a template
	if _, exists := pc.templates[name]; exists {
		return fmt.Errorf("a template with name '%s' already exists", name)
	}

	pc.prompts[name] = &Prompt{
		Prompt: schema.Prompt{
			Name:        name,
			Description: description,
			// Arguments field removed from Prompt struct in V2025 schema
		},
		Handler:      handler,
		LastModified: time.Now(),
	}

	pc.logger.Info("Added prompt", zap.String("name", name))
	go pc.broadcastPromptsChanged() // Notify clients
	return nil
}

// UpdatePrompt updates an existing prompt.
func (pc *PromptsCapability) UpdatePrompt(name string, description string, handler PromptHandler) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	prompt, exists := pc.prompts[name]
	if !exists {
		return fmt.Errorf("prompt with name '%s' does not exist", name)
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
		return fmt.Errorf("prompt with name '%s' does not exist", name)
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
		return fmt.Errorf("template with name '%s' already exists", name)
	}
	// Check if name conflicts with a regular prompt
	if _, exists := pc.prompts[name]; exists {
		return fmt.Errorf("a prompt with name '%s' already exists", name)
	}

	pc.templates[name] = &Template{
		Prompt: schema.Prompt{
			Name:        name,
			Description: description,
			Arguments:   arguments, // Arguments are part of the template definition in V2025
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
		return fmt.Errorf("template with name '%s' does not exist", name)
	}

	delete(pc.templates, name)
	pc.logger.Info("Deleted prompt template", zap.String("name", name))
	go pc.broadcastPromptsChanged() // Notify clients
	return nil
}

// broadcastPromptsChanged sends a "notifications/prompts/list_changed" notification to eligible sessions.
func (pc *PromptsCapability) broadcastPromptsChanged() {
	if pc.manager == nil {
		pc.logger.Error("Cannot broadcast prompt list changed: manager not set")
		return
	}
	// Params are optional for this notification
	pc.manager.NotifyEligibleSessions("notifications/prompts/list_changed", nil)
	pc.logger.Debug("Broadcasted prompts list changed notification")
}

// handlePromptsList handles the "prompts/list" request from the client.
func (pc *PromptsCapability) handlePromptsList(msg *shared.Message) (interface{}, error) {
	logger := pc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "prompts/list"))
	logger.Debug("Handling prompts list request")

	pc.mu.RLock()
	defer pc.mu.RUnlock()

	// Parse pagination parameters if any (using V2025 structure)
	var params schema.ListPromptsRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil {
			logger.Warn("Failed to unmarshal pagination params", zap.Error(err))
			// Ignore pagination error and return the first page? Or return error?
			// Let's return an error for invalid params.
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}
	// TODO: Implement actual pagination based on params.Cursor

	// Combine regular prompts and templates into a single list
	allPrompts := make([]schema.Prompt, 0, len(pc.prompts)+len(pc.templates))

	for _, prompt := range pc.prompts {
		allPrompts = append(allPrompts, prompt.Prompt) // Add embedded V2025 Prompt
	}
	for _, template := range pc.templates {
		allPrompts = append(allPrompts, template.Prompt) // Add embedded V2025 Prompt
	}

	// Sort prompts/templates alphabetically by name for consistent ordering? (Optional)
	// sort.Slice(allPrompts, func(i, j int) bool { return allPrompts[i].Name < allPrompts[j].Name })

	// Apply pagination logic here based on params.Cursor and allPrompts

	// For now, return all combined prompts without pagination
	result := schema.ListPromptsResult{
		Prompts: allPrompts,
		PaginatedResult: schema.PaginatedResult{
			NextCursor: nil, // Set NextCursor if pagination is implemented and there are more items
		},
		// Meta: Add metadata if needed
	}

	logger.Debug("Returning prompt list", zap.Int("count", len(result.Prompts)))
	return result, nil
}

// handlePromptsGet handles the "prompts/get" request from the client.
func (pc *PromptsCapability) handlePromptsGet(msg *shared.Message) (interface{}, error) {
	logger := pc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "prompts/get"))

	// Parse parameters using V2025 schema type
	var params schema.GetPromptRequestParams
	if msg.Params == nil {
		logger.Warn("Missing parameters in prompts/get request")
		return nil, fmt.Errorf("invalid request: missing params")
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal prompts/get params", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	logger = logger.With(zap.String("promptName", params.Name))
	logger.Debug("Handling get prompt request")

	pc.mu.RLock()
	// First check regular prompts
	prompt, promptExists := pc.prompts[params.Name]
	// Then check templates
	template, templateExists := pc.templates[params.Name]
	pc.mu.RUnlock()

	var handler PromptHandler
	var description string

	if promptExists {
		logger.Debug("Found matching regular prompt")
		handler = prompt.Handler
		description = prompt.Description
	} else if templateExists {
		logger.Debug("Found matching prompt template")
		handler = template.Handler
		description = template.Description
		// TODO: Potentially validate provided arguments against template.Arguments definition?
	} else {
		logger.Warn("Prompt/template not found")
		return nil, fmt.Errorf("prompt or template with name '%s' not found", params.Name)
	}

	if handler == nil {
		logger.Error("Found prompt/template but handler is nil", zap.String("name", params.Name))
		return nil, fmt.Errorf("internal error: handler not available for '%s'", params.Name)
	}

	// Call the handler associated with the prompt or template
	logger.Debug("Calling handler")
	meta, messages, err := handler(msg) // Pass the original message containing session and potentially arguments
	if err != nil {
		logger.Error("Prompt handler returned an error", zap.Error(err))
		return nil, fmt.Errorf("handler for '%s' failed: %w", params.Name, err)
	}

	// Construct the V2025 result
	result := schema.GetPromptResult{
		Meta:        meta,
		Description: description, // Use description from the found prompt/template
		Messages:    messages,
	}

	logger.Debug("Successfully generated prompt content")
	return result, nil
}
