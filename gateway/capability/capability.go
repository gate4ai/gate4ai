// gateway/capability/capability.go
package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	client "github.com/gate4ai/gate4ai/gateway/clients/mcpClient"
	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared"
	"github.com/gate4ai/gate4ai/shared/config"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

const defaultCacheExpiration = 5 * time.Second

// ServerConnection represents a connection to a remote SSE server
type ServerConnection struct {
	URL       string
	Session   *client.Session
	Connected bool
}

var _ shared.IServerCapability = (*GatewayCapability)(nil)

// GatewayCapability implements server routing for a user
type GatewayCapability struct {
	logger       *zap.Logger
	ctx          context.Context
	cancel       context.CancelFunc
	refreshRate  time.Duration
	userSessions map[string]*transport.Session // UserID -> mcp session
	config       config.IConfig
}

// NewGatewayCapability creates a new gateway capability
func NewGatewayCapability(logger *zap.Logger, cfg config.IConfig) *GatewayCapability {
	ctx, cancel := context.WithCancel(context.Background())
	cap := &GatewayCapability{
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
		refreshRate:  5 * time.Minute,
		userSessions: make(map[string]*transport.Session),
		config:       cfg,
	}
	return cap
}

func (c *GatewayCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	handlers := make(map[string]func(*shared.Message) (interface{}, error))
	handlers["completion/complete"] = c.gw_completion_complete
	handlers["prompts/list"] = c.gw_prompts_list
	handlers["prompts/get"] = c.gw_prompts_get
	handlers["resources/list"] = c.gw_resources_list
	handlers["resources/read"] = c.gw_resources_read
	handlers["resources/subscribe"] = c.gw_resources_subscribe
	handlers["resources/unsubscribe"] = c.gw_resources_unsubscribe
	handlers["tools/list"] = c.gw_tools_list
	handlers["tools/call"] = c.gw_tools_call
	return handlers
}

// NEW: mergeHeaders merges headers with specific priority: System > Server > Subscription
func mergeHeaders(system, server, subscription map[string]string) map[string]string {
	merged := make(map[string]string)

	// 1. Subscription headers (lowest priority)
	for k, v := range subscription {
		merged[strings.ToLower(k)] = v
	}

	// 2. Server headers (override subscription)
	for k, v := range server {
		merged[strings.ToLower(k)] = v
	}

	// 3. System headers (highest priority, override server/subscription)
	for k, v := range system {
		lowerK := strings.ToLower(k)
		merged[lowerK] = v
	}

	return merged
}

// getMergedHeaders retrieves and merges headers for a given session and server.
func (c *GatewayCapability) getMergedHeaders(clientSession shared.ISession, serverSlug string) map[string]string {
	logger := c.logger.With(zap.String("sessionID", clientSession.GetID()), zap.String("serverSlug", serverSlug))

	// 1. System Headers
	systemHeaders := make(map[string]string)
	userID := transport.GetUserId(clientSession.GetParams())
	remoteAddr := transport.GetRemoteAddr(clientSession.GetParams()) // Assumes RemoteAddr is stored

	if userID != "" {
		systemHeaders["Gate4ai-User-Id"] = userID
	}
	if serverSlug != "" {
		systemHeaders["Gate4ai-Server-Slug"] = serverSlug
	}
	headersValue, ok := clientSession.GetParams().Load(transport.HEADERKEY)
	if !ok {
		logger.Warn("Gateway received headers not found in session parameters")
	} else {
		header, ok := headersValue.(http.Header)
		if !ok {
			logger.Error("Gateway received headers found but have incorrect type", zap.Any("type", fmt.Sprintf("%T", headersValue)))
		}
		systemHeaders["X-Forwarded-For"] = header.Get("X-Forwarded-For")
		if remoteAddr != "" {
			systemHeaders["X-Forwarded-For"] = header.Get("X-Forwarded-For") + ", " + remoteAddr
		}
	}

	// 2. Server Headers
	serverHeaders, err := c.config.GetServerHeaders(serverSlug)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		logger.Warn("Failed to get server headers", zap.Error(err))
		serverHeaders = make(map[string]string) // Use empty if error
	} else if err != nil && errors.Is(err, config.ErrNotFound) {
		serverHeaders = make(map[string]string) // Use empty if not found
	}

	// 3. Subscription Headers
	subscriptionHeaders := make(map[string]string)
	if userID != "" { // Only fetch if user is authenticated
		var subErr error
		subscriptionHeaders, subErr = c.config.GetSubscriptionHeaders(userID, serverSlug)
		if subErr != nil && !errors.Is(subErr, config.ErrNotFound) {
			logger.Warn("Failed to get subscription headers", zap.String("userID", userID), zap.Error(subErr))
			// Use empty headers on error
		} else if subErr != nil && errors.Is(subErr, config.ErrNotFound) {
			// Not found is expected if user hasn't configured them
		}
	}

	// 4. Merge
	merged := mergeHeaders(systemHeaders, serverHeaders, subscriptionHeaders)
	logger.Debug("Merged headers", zap.Any("headers", merged)) // Be careful logging headers in production
	return merged
}

// newBackendSession creates a new backend session for the given server
func (c *GatewayCapability) newBackendSession(serverSlug string, clientSession shared.ISession, logger *zap.Logger) *client.Session {
	backend, err := c.config.GetBackendBySlug(serverSlug)
	if err != nil {
		logger.Error("Failed to get backend server", zap.String("serverSlug", serverSlug), zap.Error(err))
		return nil
	}

	// Get merged headers
	mergedHeaders := c.getMergedHeaders(clientSession, serverSlug)

	backendServer, err := client.New(serverSlug, backend.URL, logger)
	if err != nil {
		logger.Error("Failed to create backend client", zap.String("serverSlug", serverSlug), zap.Error(err))
		return nil
	}

	// --- Use functional options to create session ---
	// Start with default HTTP client
	options := []client.SessionOption{
		client.WithHTTPClient(http.DefaultClient),
	}
	// Add merged headers
	options = append(options, client.WithHeaders(mergedHeaders))

	newBackendSession := backendServer.NewSession(c.ctx, options...)
	SaveServerSlug(newBackendSession.GetParams(), serverSlug)
	// clientSession is an ISession, GetParams() is available.
	// We need to pass the clientSession itself for callbacks later.
	SaveClientSession(newBackendSession.GetParams(), clientSession)
	newBackendSession.SubscribeOnResourceUpdated(c.gw_resources_notification_updated)

	return newBackendSession
}

// getBackendSession returns an existing backend session for the given server or creates a new one
// Updated to potentially refresh headers if needed (though current client design doesn't support dynamic header updates easily)
func (c *GatewayCapability) getBackendSession(clientSession shared.ISession, serverSlug string) (*client.Session, error) {
	backendSessions, err := c.getBackendSessions(clientSession)
	if err != nil {
		return nil, fmt.Errorf("failed to get backend sessions for server '%s': %w", serverSlug, err)
	}

	for _, session := range backendSessions {
		if session != nil && session.Backend != nil && session.Backend.Slug == serverSlug {
			// Optional: Check if headers need refresh? Complex with current client design.
			// For now, headers are set on creation.
			return session, nil
		}
	}
	return nil, fmt.Errorf("backend session not found for server: %s", serverSlug)
}

// getBackendSessions returns all backend sessions for the client session
// Mostly unchanged, but ensures new sessions get headers.
func (c *GatewayCapability) getBackendSessions(clientSession shared.ISession) ([]*client.Session, error) {
	logger := c.logger
	params := clientSession.GetParams()

	backendSessions, timestamp, found := LoadBackendSessions(params)
	if found && time.Since(timestamp) < defaultCacheExpiration {
		validSessions := make([]*client.Session, 0, len(backendSessions))
		for _, s := range backendSessions {
			if s != nil {
				validSessions = append(validSessions, s)
			}
		}
		return validSessions, nil
	}

	existingSessions := make(map[string]*client.Session)
	for _, session := range backendSessions {
		if session != nil && session.Backend != nil {
			existingSessions[session.Backend.Slug] = session
		}
	}

	userID := transport.GetUserId(clientSession.GetParams())
	if userID == "" {
		logger.Warn("User ID not found in client session params, cannot get subscriptions")
		return nil, fmt.Errorf("user ID not found in session")
	}

	userServers, err := c.config.GetUserSubscribes(userID)
	if err != nil {
		err = fmt.Errorf("failed to get user server subscriptions for user '%s': %w", userID, err)
		logger.Error(err.Error())
		return nil, err
	}
	logger.Debug("User subscribed servers", zap.String("userID", userID), zap.Strings("servers", userServers))

	var currentBackendSessions []*client.Session
	var wg sync.WaitGroup
	sessionChan := make(chan *client.Session, len(userServers))

	for _, serverSlug := range userServers {
		wg.Add(1)
		go func(serverSlug string) {
			defer wg.Done()
			var sess *client.Session
			if session, exists := existingSessions[serverSlug]; exists && session != nil {
				// Reuse existing. Headers won't update unless session is recreated.
				sess = session
				logger.Debug("Reusing existing backend session", zap.String("serverSlug", serverSlug))
				delete(existingSessions, serverSlug)
			} else {
				logger.Debug("Creating new backend session", zap.String("serverSlug", serverSlug))
				sess = c.newBackendSession(serverSlug, clientSession, logger.With(zap.String("serverSlug", serverSlug))) // Gets headers on creation
			}
			if sess != nil {
				sessionChan <- sess
			} else {
				logger.Error("Failed to create or reuse backend session", zap.String("serverSlug", serverSlug))
			}
		}(serverSlug)
	}

	wg.Wait()
	close(sessionChan)

	for sess := range sessionChan {
		currentBackendSessions = append(currentBackendSessions, sess)
	}

	for serverSlug, oldSession := range existingSessions {
		if oldSession != nil {
			logger.Debug("Closing unused old backend session", zap.String("serverSlug", serverSlug))
			oldSession.Close()
		}
	}

	SaveBackendSessions(params, currentBackendSessions)

	serverSlugs := make([]string, 0, len(currentBackendSessions))
	for _, s := range currentBackendSessions {
		if s != nil && s.Backend != nil {
			serverSlugs = append(serverSlugs, s.Backend.Slug)
		}
	}
	logger.Debug("Current backend sessions established", zap.Strings("serverSlugs", serverSlugs))

	return currentBackendSessions, nil
}

// fetchAndCombineFromBackends - No changes needed here, it just calls the fetchFunc which uses the session (with its headers).
func fetchAndCombineFromBackends[T any](
	c *GatewayCapability,
	ctx context.Context,
	clientSession shared.ISession,
	fetchFunc func(context.Context, *client.Session) ([]T, error), // Changed signature
	getKeyFunc func(T) string,
	modifyKeyFunc func(T, string) T,
) ([]T, error) {
	logger := c.logger
	backendSessions, err := c.getBackendSessions(clientSession)
	if err != nil {
		logger.Error("Failed to get backend sessions", zap.Error(err))
		return nil, err
	}
	logger.Debug("Fetching data from backend sessions", zap.Int("count", len(backendSessions)))

	resultsChan := make(chan struct {
		items      []T
		serverSlug string
		err        error
	}, len(backendSessions))
	var wg sync.WaitGroup

	for _, session := range backendSessions {
		if session == nil {
			logger.Warn("Skipping nil backend session")
			continue
		}
		wg.Add(1)
		go func(s *client.Session) {
			defer wg.Done()
			serverSlug := "unknown"
			if s != nil && s.Backend != nil {
				serverSlug = s.Backend.Slug
			}

			initErr := <-s.Open()
			if initErr != nil {
				logger.Error("Backend session failed to initialize", zap.String("server", serverSlug), zap.Error(initErr))
				resultsChan <- struct {
					items      []T
					serverSlug string
					err        error
				}{nil, serverSlug, fmt.Errorf("session init failed: %w", initErr)}
				return
			}
			fetchCtx, cancel := context.WithTimeout(ctx, 1000*time.Second)
			defer cancel()
			items, fetchErr := fetchFunc(fetchCtx, s) // Pass session to fetchFunc
			resultsChan <- struct {
				items      []T
				serverSlug string
				err        error
			}{items, serverSlug, fetchErr}
		}(session)
	}
	wg.Wait()
	close(resultsChan)

	allItems := make([]T, 0)
	keyToServer := make(map[string][]string)
	for result := range resultsChan {
		if result.err != nil {
			logger.Error("Failed to get data from backend", zap.String("server", result.serverSlug), zap.Error(result.err))
			continue
		}
		if result.items == nil {
			continue
		}
		for _, item := range result.items {
			key := getKeyFunc(item)
			keyToServer[key] = append(keyToServer[key], result.serverSlug)
			allItems = append(allItems, item)
		}
	}

	modifiedItems := make([]T, 0, len(allItems))
	for _, item := range allItems {
		key := getKeyFunc(item)
		serverIDs := keyToServer[key]
		if len(serverIDs) > 1 {
			var serverSlug string
			// Extract server ID based on type (ugly, needs refactor maybe)
			switch concreteItem := any(item).(type) {
			case *tool:
				serverSlug = concreteItem.serverSlug
			case *resourceWithServerInfo:
				serverSlug = concreteItem.serverSlug
			case *prompt:
				serverSlug = concreteItem.serverSlug
			default:
				logger.Error("Could not determine serverID for item during duplicate modification", zap.String("key", key), zap.Any("type", fmt.Sprintf("%T", item)))
			}
			if serverSlug == "" {
				modifiedItems = append(modifiedItems, item)
			} else {
				modifiedItems = append(modifiedItems, modifyKeyFunc(item, serverSlug))
			}
		} else {
			modifiedItems = append(modifiedItems, item)
		}
	}

	logger.Debug("Finished fetching and combining data", zap.Int("totalItems", len(modifiedItems)))
	return modifiedItems, nil
}

// findBackendSessionForResourceURI - No direct changes, relies on getBackendSession.
func (c *GatewayCapability) findBackendSessionForResourceURI(inputMsg *shared.Message, logger *zap.Logger) (*client.Session, *resourceWithServerInfo, error) {
	var params struct {
		URI string `json:"uri"`
	}
	if inputMsg.Params == nil {
		return nil, nil, fmt.Errorf("missing parameters in message")
	}
	if err := json.Unmarshal(*inputMsg.Params, &params); err != nil {
		return nil, nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.URI == "" {
		return nil, nil, fmt.Errorf("resource URI is required in parameters")
	}

	resources, err := c.GetResources(inputMsg, logger) // Assumes GetResources is defined elsewhere
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get resources: %w", err)
	}

	var targetResource *resourceWithServerInfo
	for _, res := range resources {
		if res != nil && res.URI == params.URI {
			targetResource = res
			break
		}
	}
	if targetResource == nil {
		logger.Error("Resource not found", zap.String("uri", params.URI))
		return nil, nil, fmt.Errorf("resource not found: %s", params.URI)
	}

	backendSession, err := c.getBackendSession(inputMsg.Session, targetResource.serverSlug)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get backend session for server '%s' (resource URI '%s'): %w", targetResource.serverSlug, params.URI, err)
	}
	if backendSession == nil {
		return nil, nil, fmt.Errorf("backend session resolved to nil for server '%s'", targetResource.serverSlug)
	}

	return backendSession, targetResource, nil
}

// SetCapabilities - No changes needed.
func (c *GatewayCapability) SetCapabilities(s *schema.ServerCapabilities) {
	s.Completions = &struct{}{}
	s.Prompts = &schema.Capability{ListChanged: true}
	s.Resources = &schema.CapabilityWithSubscribe{ListChanged: true, Subscribe: true}
	s.Tools = &schema.Capability{ListChanged: true}
}

// Helper function to associate client session with backend session parameters
func SaveClientSession(sessionParams *sync.Map, clientSession shared.ISession) {
	sessionParams.Store(clientSessionsKey, &SavedValue{
		Value:     clientSession,
		Timestamp: time.Now(),
	})
}

// Helper function to retrieve client session from backend session parameters
func GetClientSession(sessionParams *sync.Map) (shared.ISession, time.Time, bool) {
	savedValue, ok1 := sessionParams.Load(clientSessionsKey)
	if !ok1 {
		return nil, time.Time{}, false
	}

	saved, ok2 := savedValue.(*SavedValue)
	if !ok2 {
		return nil, time.Time{}, false
	}

	session, ok := saved.Value.(shared.ISession)
	if !ok {
		return nil, time.Time{}, false
	}

	return session, saved.Timestamp, true
}
