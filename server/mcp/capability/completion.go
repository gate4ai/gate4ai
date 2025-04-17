package capability

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gate4ai/gate4ai/shared"

	// Use 2025 schema for completion structures
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

type completionRefBase struct {
	Type string `json:"type"`
}

// PromptReference for completion.
type completionPromptRef struct {
	completionRefBase
	Name string `json:"name"` // Name of the prompt
}

// ResourceReference for completion.
type completionResourceRef struct {
	completionRefBase
	URI string `json:"uri"` // URI of the resource
}

type CompletionHandler func(msg *shared.Message, arg schema.CompleteArgument) (*schema.CompletionInfo, error)

var _ shared.IServerCapability = (*CompletionCapability)(nil)

// CompletionCapability handles completion requests for prompts and resources.
type CompletionCapability struct {
	logger             *zap.Logger
	mu                 sync.RWMutex
	promptCompleters   map[string]CompletionHandler                          // Map prompt name -> handler
	resourceCompleters map[string]CompletionHandler                          // Map resource URI (or pattern) -> handler
	handlers           map[string]func(*shared.Message) (interface{}, error) // Map method -> handler function
}

// NewCompletionCapability creates a new instance of the CompletionCapability
func NewCompletionCapability(logger *zap.Logger) *CompletionCapability {
	cc := &CompletionCapability{
		logger:             logger,
		promptCompleters:   make(map[string]CompletionHandler),
		resourceCompleters: make(map[string]CompletionHandler),
	}
	cc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"completion/complete": cc.handleCompletionComplete,
	}

	return cc
}

func (cc *CompletionCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return cc.handlers
}

func (cc *CompletionCapability) SetCapabilities(s *schema.ServerCapabilities) {
	s.Completions = &struct{}{}
}

// AddPromptCompleter adds a completer for a specific prompt name.
func (cc *CompletionCapability) AddPromptCompleter(promptName string, handler CompletionHandler) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if handler == nil {
		cc.logger.Warn("Attempted to add nil handler for prompt completer", zap.String("promptName", promptName))
		return
	}
	cc.promptCompleters[promptName] = handler
	cc.logger.Info("Added prompt completer", zap.String("promptName", promptName))
}

func (cc *CompletionCapability) AddResourceCompleter(resourceURI string, handler CompletionHandler) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if handler == nil {
		cc.logger.Warn("Attempted to add nil handler for resource completer", zap.String("resourceURI", resourceURI))
		return
	}
	cc.resourceCompleters[resourceURI] = handler
	cc.logger.Info("Added resource completer", zap.String("resourceURI", resourceURI))
}

// RemovePromptCompleter removes a prompt completer.
func (cc *CompletionCapability) RemovePromptCompleter(promptName string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if _, exists := cc.promptCompleters[promptName]; exists {
		delete(cc.promptCompleters, promptName)
		cc.logger.Info("Removed prompt completer", zap.String("promptName", promptName))
	}
}

// RemoveResourceCompleter removes a resource completer.
func (cc *CompletionCapability) RemoveResourceCompleter(resourceURI string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if _, exists := cc.resourceCompleters[resourceURI]; exists {
		delete(cc.resourceCompleters, resourceURI)
		cc.logger.Info("Removed resource completer", zap.String("resourceURI", resourceURI))
	}
}

// findResourceCompleter finds the most specific resource completer for a URI.
// Currently only supports exact matches.
func (cc *CompletionCapability) findResourceCompleter(uri string) (CompletionHandler, bool) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()

	// Exact match check
	if handler, exists := cc.resourceCompleters[uri]; exists {
		return handler, true
	}

	// TODO: Add pattern matching (e.g., prefix matching, wildcard matching)
	// Example: iterate through patterns and find the longest matching one.

	return nil, false
}

// handleCompletionComplete handles the "completion/complete" request.
func (cc *CompletionCapability) handleCompletionComplete(msg *shared.Message) (interface{}, error) {
	logger := cc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "completion/complete"))
	logger.Debug("Handling completion request")

	// Parse parameters (V2025)
	var params schema.CompletionRequestParams
	if msg.Params == nil {
		logger.Warn("Missing parameters in completion request")
		return nil, fmt.Errorf("invalid request: missing params")
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal completion params", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	// Determine the reference type (prompt or resource) by unmarshalling the Ref field
	var refType completionRefBase
	if err := json.Unmarshal(params.Ref, &refType); err != nil {
		logger.Error("Failed to unmarshal completion reference type", zap.Error(err))
		return nil, fmt.Errorf("invalid reference in parameters: %w", err)
	}

	var handler CompletionHandler
	var exists bool
	var refIdentifier string // For logging

	switch refType.Type {
	case "ref/prompt":
		var promptRef completionPromptRef
		if err := json.Unmarshal(params.Ref, &promptRef); err != nil {
			logger.Error("Failed to unmarshal prompt reference", zap.Error(err))
			return nil, fmt.Errorf("invalid prompt reference: %w", err)
		}
		refIdentifier = promptRef.Name
		logger.Debug("Completion requested for prompt", zap.String("promptName", refIdentifier))
		cc.mu.RLock()
		handler, exists = cc.promptCompleters[refIdentifier]
		cc.mu.RUnlock()
		if !exists {
			logger.Warn("No completion handler found for prompt", zap.String("promptName", refIdentifier))
			return nil, fmt.Errorf("no completion handler for prompt: %s", refIdentifier)
		}

	case "ref/resource":
		var resourceRef completionResourceRef
		if err := json.Unmarshal(params.Ref, &resourceRef); err != nil {
			logger.Error("Failed to unmarshal resource reference", zap.Error(err))
			return nil, fmt.Errorf("invalid resource reference: %w", err)
		}
		refIdentifier = resourceRef.URI
		logger.Debug("Completion requested for resource", zap.String("resourceURI", refIdentifier))
		handler, exists = cc.findResourceCompleter(refIdentifier)
		if !exists {
			logger.Warn("No completion handler found for resource", zap.String("resourceURI", refIdentifier))
			return nil, fmt.Errorf("no completion handler for resource: %s", refIdentifier)
		}

	default:
		logger.Warn("Unsupported completion reference type", zap.String("type", refType.Type))
		return nil, fmt.Errorf("unsupported reference type: %s", refType.Type)
	}

	// Call the appropriate handler
	logger.Debug("Calling completion handler",
		zap.String("refType", refType.Type),
		zap.String("refIdentifier", refIdentifier),
		zap.String("argName", params.Argument.Name),
		zap.String("argValue", params.Argument.Value))

	completionInfo, err := handler(msg, params.Argument)
	if err != nil {
		logger.Error("Completion handler returned an error", zap.Error(err))
		return nil, fmt.Errorf("completion handler failed: %w", err) // Propagate handler error
	}

	if completionInfo == nil {
		// Handler should return empty info, not nil
		logger.Warn("Completion handler returned nil info, returning empty result")
		completionInfo = &schema.CompletionInfo{Values: []string{}}
	}

	// Construct the V2025 result
	result := schema.CompleteResult{
		Completion: *completionInfo,
		// Meta: Add metadata if needed
	}

	logger.Debug("Completion successful", zap.Int("suggestionCount", len(result.Completion.Values)))
	return result, nil
}

// GetDefaultCompletionResult returns an empty completion result.
// Useful for handlers that have no suggestions.
func GetDefaultCompletionResult() *schema.CompleteResult {
	return &schema.CompleteResult{
		Completion: schema.CompletionInfo{
			Values: []string{}, // Empty slice
			// HasMore and Total can be omitted (nil)
		},
	}
}

// GetDefaultCompletionInfo returns empty completion info.
func GetDefaultCompletionInfo() *schema.CompletionInfo {
	return &schema.CompletionInfo{
		Values: []string{},
	}
}
