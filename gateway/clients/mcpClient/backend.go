package mcpClient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gate4ai/gate4ai/gateway/clients/mcpClient/capability"
	"github.com/gate4ai/gate4ai/shared"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"github.com/r3labs/sse/v2"
	"go.uber.org/zap"
)

type Backend struct {
	Slug   string
	URL    *url.URL
	Logger *zap.Logger
}

// New creates a new MCP SSE client backend
func New(serverSlug string, sseurl string, logger *zap.Logger) (*Backend, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	u, err := url.Parse(sseurl)
	if err != nil {
		return nil, fmt.Errorf("invalid SSE URL %s: %w", sseurl, err)
	}

	logger = logger.With(zap.String("backendSlug", serverSlug), zap.String("backendURL", u.String()))
	logger.Debug("Created new MCP SSE client backend")

	return &Backend{Slug: serverSlug, URL: u, Logger: logger}, nil
}

// NewSession creates a new session, configuring it with functional options.
func (backend *Backend) NewSession(ctx context.Context, opts ...SessionOption) *Session {
	input := shared.NewInput(backend.Logger)
	baseSession := shared.NewBaseSession(backend.Logger, "", input, nil)
	baseSession.Logger.Debug("Creating new client session", zap.String("backendSlug", backend.Slug))

	clientSession := &Session{
		ctx:            ctx,
		BaseSession:    baseSession,
		Backend:        backend,
		sseCh:          make(chan *sse.Event, 100),
		closeCh:        make(chan struct{}),
		initialization: nil, // Start as nil, set in Open()
		tools:          make([]schema.Tool, 0),
		prompts:        make([]schema.Prompt, 0),
		resources:      make([]schema.Resource, 0),
		inputProcessor: input,
		httpClient:     http.DefaultClient,      // Start with default client
		currentHeaders: make(map[string]string), // Start with empty headers
	}

	sseClient := sse.NewClient(backend.URL.String())
	clientSession.sseClient = sseClient // Assign SSE client early

	// Apply functional options to configure httpClient and currentHeaders
	err := applySessionOptions(clientSession, opts)
	if err != nil {
		// Log the error and potentially return a non-functional session or panic?
		// For now, log and continue, the session might be partially configured.
		baseSession.Logger.Error("Failed to apply session options", zap.Error(err))
	}

	// Set headers for the SSE client connection *after* applying options
	sseClient.Headers = make(map[string]string)
	for k, v := range clientSession.currentHeaders {
		sseClient.Headers[k] = v
	}

	// Ensure standard SSE headers aren't overwritten accidentally
	sseClient.Headers["Accept"] = "text/event-stream"
	sseClient.Headers["Cache-Control"] = "no-cache"
	sseClient.Headers["Connection"] = "keep-alive"
	// Authorization should be part of currentHeaders if needed

	// Initialize capabilities
	resourcesCap := capability.NewResourcesCapability(backend.Logger, clientSession)
	resourceTemplatesCap := capability.NewResourceTemplatesCapability(backend.Logger, clientSession)
	samplingCap := capability.NewSamplingCapability(backend.Logger)

	input.AddClientCapability(resourcesCap, resourceTemplatesCap, samplingCap)

	clientSession.ResourcesCapability = resourcesCap
	clientSession.ResourceTemplatesCapability = resourceTemplatesCap
	clientSession.SamplingCapability = samplingCap

	go input.Process()
	baseSession.Logger.Info("Client session created", zap.Int("finalHeaderCount", len(clientSession.currentHeaders)))
	return clientSession
}
