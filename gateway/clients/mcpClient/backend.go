package mcpClient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gate4ai/mcp/gateway/clients/mcpClient/capability"
	"github.com/gate4ai/mcp/shared"
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"github.com/r3labs/sse/v2"
	"go.uber.org/zap"
)

type Backend struct {
	Slug   string
	URL    *url.URL
	Logger *zap.Logger
}

// New creates a new MCP SSE client
func New(serverSlug string, sseurl string, logger *zap.Logger) (*Backend, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	u, err := url.Parse(sseurl)
	if err != nil {
		logger.Error("Failed to parse SSE URL", zap.String("url", sseurl), zap.Error(err))
		return nil, fmt.Errorf("invalid SSE URL %s: %w", sseurl, err) // Wrap error
	}

	logger = logger.With(zap.String("backendSlug", serverSlug), zap.String("backendURL", u.String())) // Add context to logger
	logger.Debug("Created new MCP SSE client backend")

	return &Backend{
		Slug:   serverSlug,
		URL:    u,
		Logger: logger,
	}, nil
}

// NewSession creates a new session
func (backend *Backend) NewSession(ctx context.Context, httpClient *http.Client, authorizationBearer string) *Session {
	input := shared.NewInput(backend.Logger)

	baseSession := shared.NewBaseSession(backend.Logger, input, nil)
	baseSession.Logger.Debug("Creating new client session")

	sseClient := sse.NewClient(backend.URL.String())
	// Assign logger to SSE client if it supports it (optional, depends on library)
	// sseClient.Logger = sessionLogger // Example, adjust based on sse/v2 capabilities

	sseClient.Headers = map[string]string{
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
		"Connection":    "keep-alive", // Good practice for SSE
	}
	if authorizationBearer != "" {
		sseClient.Headers["Authorization"] = "Bearer " + authorizationBearer
	}

	// Use default client if nil is provided
	if httpClient == nil {
		baseSession.Logger.Debug("Using default HTTP client for session")
		httpClient = http.DefaultClient
	}

	// Create the specific client Session
	clientSession := &Session{
		ctx:            ctx,
		BaseSession:    baseSession,
		Backend:        backend,
		sseClient:      sseClient,
		httpClient:     httpClient,
		sseCh:          make(chan *sse.Event, 100), // Consider buffer size
		closeCh:        make(chan struct{}),        // Initialize close channel
		initialization: nil,                        // Start as nil, set in Open()
		tools:          make([]schema.Tool, 0),
		prompts:        make([]schema.Prompt, 0),
		resources:      make([]schema.Resource, 0),
		inputProcessor: input,
	}

	resourcesCap := capability.NewResourcesCapability(backend.Logger, clientSession)
	resourceTemplatesCap := capability.NewResourceTemplatesCapability(backend.Logger, clientSession)
	samplingCap := capability.NewSamplingCapability(backend.Logger)

	input.AddClientCapability(
		resourcesCap,
		resourceTemplatesCap,
		samplingCap)

	clientSession.ResourcesCapability = resourcesCap
	clientSession.ResourceTemplatesCapability = resourceTemplatesCap
	clientSession.SamplingCapability = samplingCap

	go input.Process()
	baseSession.Logger.Info("Client session created")
	return clientSession
}
