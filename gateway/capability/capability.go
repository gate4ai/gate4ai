package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gate4ai/mcp/gateway/client"
	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared"
	"github.com/gate4ai/mcp/shared/config"
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema" // Use 2025 schema
	"go.uber.org/zap"
)

// Cache expiration time
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
	userSessions map[string]*mcp.Session // UserID -> mcp session
	config       config.IConfig
}

// NewGatewayCapability creates a new gateway capability
func NewGatewayCapability(logger *zap.Logger, cfg config.IConfig) *GatewayCapability {
	ctx, cancel := context.WithCancel(context.Background())

	cap := &GatewayCapability{
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
		refreshRate:  5 * time.Minute, // Default refresh rate
		userSessions: make(map[string]*mcp.Session),
		config:       cfg,
	}
	return cap
}

func (c *GatewayCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	handlers := make(map[string]func(*shared.Message) (interface{}, error))

	// Register all handlers that the gateway implements
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

// newBackendSession creates a new backend session for the given server
func (c *GatewayCapability) newBackendSession(serverID string, clientSession shared.ISession, logger *zap.Logger) *client.Session {
	// Get the backend server by ID
	backend, err := c.config.GetBackend(serverID)
	if err != nil {
		logger.Error("Failed to get backend server", zap.String("serverID", serverID), zap.Error(err))
		return nil
	}

	backendServer, err := client.New(serverID, backend.URL, logger)
	if err != nil {
		logger.Error("Failed to create backend client", zap.String("server", serverID), zap.Error(err))
		return nil
	}

	newBackendSession := backendServer.NewSession(c.ctx, http.DefaultClient, backend.Bearer)
	SaveServerID(newBackendSession.GetParams(), serverID)                          // Use GetParams()
	SaveClientSession(newBackendSession.GetParams(), clientSession.(*mcp.Session)) // Use GetParams()
	newBackendSession.SubscribeOnResourceUpdated(c.gw_resources_notification_updated)

	return newBackendSession
}

// getBackendSession returns an existing backend session for the given server or creates a new one
func (c *GatewayCapability) getBackendSession(clientSession shared.ISession, serverID string) (*client.Session, error) {
	backendSessions, err := c.getBackendSessions(clientSession)
	if err != nil {
		// Wrap error for better context
		return nil, fmt.Errorf("failed to get backend sessions for server '%s': %w", serverID, err)
	}

	// Look for an existing session for the requested server
	for _, session := range backendSessions {
		if session != nil && session.Backend != nil && session.Backend.ID == serverID {
			return session, nil
		}
	}

	return nil, fmt.Errorf("backend session not found for server: %s", serverID)
}

// getBackendSessions returns all backend sessions for the client session
func (c *GatewayCapability) getBackendSessions(clientSession shared.ISession) ([]*client.Session, error) {
	logger := c.logger
	params := clientSession.GetParams()

	// Check if we already have backend sessions cached
	backendSessions, timestamp, found := LoadBackendSessions(params)
	if found && time.Since(timestamp) < defaultCacheExpiration {
		// Filter out nil sessions from cache before returning
		validSessions := make([]*client.Session, 0, len(backendSessions))
		for _, s := range backendSessions {
			if s != nil {
				validSessions = append(validSessions, s)
			}
		}
		return validSessions, nil
	}

	// Get existing sessions by mapping
	existingSessions := make(map[string]*client.Session)
	for _, session := range backendSessions {
		if session != nil && session.Backend != nil { // Add nil checks
			existingSessions[session.Backend.ID] = session
		}
	}

	// Get the list of servers the user is subscribed to
	userID := transport.GetUserId(clientSession.GetParams())
	if userID == "" {
		// Handle cases where user ID might not be available (e.g., anonymous access if allowed)
		// Depending on policy, return error or empty list
		logger.Warn("User ID not found in client session params, cannot get subscriptions")
		return nil, fmt.Errorf("user ID not found in session")
	}
	userServers, err := c.config.GetUserSubscribes(userID)
	if err != nil {
		err = fmt.Errorf("failed to get user server subscriptions for user '%s': %w", userID, err)
		logger.Error(err.Error(), zap.Error(err))
		return nil, err
	}
	logger.Debug("User subscribed servers", zap.String("userID", userID), zap.Strings("servers", userServers))

	// Create or reuse sessions for each server
	var currentBackendSessions []*client.Session
	var wg sync.WaitGroup
	sessionChan := make(chan *client.Session, len(userServers)) // Buffered channel

	for _, serverID := range userServers {
		wg.Add(1)
		go func(sID string) {
			defer wg.Done()
			var sess *client.Session
			if session, exists := existingSessions[sID]; exists && session != nil { // Check if session exists and is not nil
				// TODO: Add a check here to see if the existing session is still valid/connected
				// If not valid, create a new one instead of reusing.
				sess = session
				logger.Debug("Reusing existing backend session", zap.String("serverID", sID))
				delete(existingSessions, sID) // Remove from map to track unused old sessions
			} else {
				logger.Debug("Creating new backend session", zap.String("serverID", sID))
				sess = c.newBackendSession(sID, clientSession, logger.With(zap.String("serverID", sID)))
				// No need to call Open() here, fetchAndCombineFromBackends will handle it
			}
			if sess != nil { // Only send non-nil sessions to the channel
				sessionChan <- sess
			} else {
				logger.Error("Failed to create or reuse backend session", zap.String("serverID", sID))
			}
		}(serverID)
	}

	wg.Wait()
	close(sessionChan)

	for sess := range sessionChan {
		currentBackendSessions = append(currentBackendSessions, sess)
	}

	// Close any old sessions that are no longer needed
	for serverID, oldSession := range existingSessions {
		if oldSession != nil {
			logger.Debug("Closing unused old backend session", zap.String("serverID", serverID))
			oldSession.Close() // Assuming Close is safe to call multiple times
		}
	}

	SaveBackendSessions(params, currentBackendSessions)

	// Log server IDs from sessions
	serverIDs := make([]string, 0, len(currentBackendSessions))
	for _, s := range currentBackendSessions {
		if s != nil && s.Backend != nil { // Add nil checks
			serverIDs = append(serverIDs, s.Backend.ID)
		}
	}
	logger.Debug("Current backend sessions established", zap.Strings("serverIDs", serverIDs))

	return currentBackendSessions, nil
}

func fetchAndCombineFromBackends[T any](
	c *GatewayCapability,
	ctx context.Context,
	clientSession shared.ISession,
	fetchFunc func(context.Context, *client.Session) ([]T, error),
	getKeyFunc func(T) string,
	modifyKeyFunc func(T, string) T, // Takes original item and serverID
) ([]T, error) {
	logger := c.logger

	backendSessions, err := c.getBackendSessions(clientSession)
	if err != nil {
		logger.Error("Failed to get backend sessions", zap.Error(err))
		return nil, fmt.Errorf("failed to get backend sessions: %w", err)
	}

	logger.Debug("Fetching data from backend sessions", zap.Int("count", len(backendSessions)))

	resultsChan := make(chan struct {
		items    []T
		serverID string
		err      error
	}, len(backendSessions))
	var wg sync.WaitGroup

	for _, session := range backendSessions {
		if session == nil { // Skip nil sessions
			logger.Warn("Skipping nil backend session")
			continue
		}
		wg.Add(1)
		go func(s *client.Session) {
			defer wg.Done()
			serverID := "unknown"
			if s != nil && s.Backend != nil {
				serverID = s.Backend.ID
			}

			// Ensure session is open before fetching
			initErr := <-s.Open() // Wait for initialization or failure
			if initErr != nil {
				logger.Error("Backend session failed to initialize", zap.String("server", serverID), zap.Error(initErr))
				resultsChan <- struct {
					items    []T
					serverID string
					err      error
				}{nil, serverID, fmt.Errorf("session init failed: %w", initErr)}
				return
			}

			// Use a derived context with the overall timeout for the fetch operation
			fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second) // Example: 10-second timeout per backend fetch
			defer cancel()

			// Fetch data from this backend
			items, fetchErr := fetchFunc(fetchCtx, s)
			resultsChan <- struct {
				items    []T
				serverID string
				err      error
			}{items, serverID, fetchErr}

		}(session)
	}

	wg.Wait()
	close(resultsChan)

	allItems := make([]T, 0)
	keyToServer := make(map[string][]string) // Map key -> list of serverIDs that have this key

	for result := range resultsChan {
		if result.err != nil {
			// Log errors but potentially continue to combine results from other backends
			logger.Error("Failed to get data from backend", zap.String("server", result.serverID), zap.Error(result.err))
			continue // Skip results from failed backends
		}
		if result.items == nil {
			continue // Skip if no items were returned (e.g., empty list)
		}
		for _, item := range result.items {
			key := getKeyFunc(item)
			keyToServer[key] = append(keyToServer[key], result.serverID)
			allItems = append(allItems, item)
		}
	}

	// Modify keys for duplicates
	modifiedItems := make([]T, 0, len(allItems))
	for _, item := range allItems {
		key := getKeyFunc(item)
		serverIDs := keyToServer[key]
		if len(serverIDs) > 1 {
			var itemServerID string
			if ts, ok := any(item).(*tool); ok {
				itemServerID = ts.serverID
			} else if rs, ok := any(item).(*resourceWithServerInfo); ok {
				itemServerID = rs.serverID
			} else if ps, ok := any(item).(*prompt); ok {
				itemServerID = ps.serverID
			}
			// TODO Add other types

			if itemServerID == "" {
				logger.Error("Could not determine serverID for item during duplicate modification", zap.String("key", key))
				// Skip modification or handle error
				modifiedItems = append(modifiedItems, item) // Add unmodified
			} else {
				modifiedItems = append(modifiedItems, modifyKeyFunc(item, itemServerID)) // Pass serverID
			}
		} else {
			modifiedItems = append(modifiedItems, item) // No conflict, add as is
		}
	}

	logger.Debug("Finished fetching and combining data", zap.Int("totalItems", len(modifiedItems)))
	return modifiedItems, nil
}

// findBackendSessionForResourceURI finds the backend session and resource for a given URI
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

	// Use GetResources which handles caching and fetching
	resources, err := c.GetResources(inputMsg, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get resources: %w", err)
	}

	var targetResource *resourceWithServerInfo
	for _, res := range resources {
		if res != nil && res.URI == params.URI { // Add nil check
			targetResource = res
			break
		}
	}

	if targetResource == nil {
		logger.Error("Resource not found", zap.String("uri", params.URI))
		return nil, nil, fmt.Errorf("resource not found: %s", params.URI)
	}

	backendSession, err := c.getBackendSession(inputMsg.Session, targetResource.serverID)
	if err != nil {
		// Provide more context in the error message
		return nil, nil, fmt.Errorf("failed to get backend session for server '%s' (resource URI '%s'): %w", targetResource.serverID, params.URI, err)
	}

	if backendSession == nil {
		// This case should ideally be covered by the error above, but added for robustness
		return nil, nil, fmt.Errorf("backend session resolved to nil for server '%s'", targetResource.serverID)
	}

	return backendSession, targetResource, nil
}

// SetCapabilities implements the shared.IServerCapability interface
func (c *GatewayCapability) SetCapabilities(s *schema.ServerCapabilities) {
	s.Completions = &struct{}{}
	s.Prompts = &schema.Capability{
		ListChanged: true,
	}
	s.Resources = &schema.CapabilityWithSubscribe{
		ListChanged: true,
		Subscribe:   true,
	}
	s.Tools = &schema.Capability{
		ListChanged: true,
	}
}
