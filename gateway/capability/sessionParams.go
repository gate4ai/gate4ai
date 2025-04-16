package capability

import (
	"sync"
	"time"

	"github.com/gate4ai/mcp/gateway/clients/mcpClient"
	"github.com/gate4ai/mcp/server/mcp"
)

// Constants for session parameter keys
const (
	backendSessionsKey = "gw_backend_sessions"
	clientSessionsKey  = "gw_client_sessions"
	serverSlugKey      = "gw_server_id"
)

// SavedValue represents a cached value with its timestamp
type SavedValue struct {
	Value     interface{}
	Timestamp time.Time
}

func SaveBackendSessions(sessionParams *sync.Map, clientSessions []*mcpClient.Session) {
	sessionParams.Store(backendSessionsKey, &SavedValue{
		Value:     clientSessions,
		Timestamp: time.Now(),
	})
}

// LoadBackendSessions returns backend sessions with timestamp and success indicator
func LoadBackendSessions(sessionParams *sync.Map) ([]*mcpClient.Session, time.Time, bool) {
	savedValue, ok1 := sessionParams.Load(backendSessionsKey)
	if !ok1 {
		return []*mcpClient.Session{}, time.Time{}, false
	}

	saved, ok2 := savedValue.(*SavedValue)
	if !ok2 {
		return []*mcpClient.Session{}, time.Time{}, false
	}

	sessions, ok := saved.Value.([]*mcpClient.Session)
	if !ok {
		return []*mcpClient.Session{}, time.Time{}, false
	}

	return sessions, saved.Timestamp, true
}

func SaveClientSession(sessionParams *sync.Map, clientSession *mcp.Session) {
	sessionParams.Store(clientSessionsKey, &SavedValue{
		Value:     clientSession,
		Timestamp: time.Now(),
	})
}

// GetClientSession returns client session with timestamp and success indicator
func GetClientSession(sessionParams *sync.Map) (*mcp.Session, time.Time, bool) {
	savedValue, ok1 := sessionParams.Load(clientSessionsKey)
	if !ok1 {
		return nil, time.Time{}, false
	}

	saved, ok2 := savedValue.(*SavedValue)
	if !ok2 {
		return nil, time.Time{}, false
	}

	session, ok := saved.Value.(*mcp.Session)
	if !ok {
		return nil, time.Time{}, false
	}

	return session, saved.Timestamp, true
}

func SaveServerSlug(sessionParams *sync.Map, serverSlug string) {
	sessionParams.Store(serverSlugKey, &SavedValue{
		Value:     serverSlug,
		Timestamp: time.Now(),
	})
}

// GetServerID returns server ID with timestamp and success indicator
func GetServerSlug(sessionParams *sync.Map) (string, time.Time, bool) {
	savedValue, ok1 := sessionParams.Load(serverSlugKey)
	if !ok1 {
		return "", time.Time{}, false
	}

	saved, ok2 := savedValue.(*SavedValue)
	if !ok2 {
		return "", time.Time{}, false
	}

	serverSlug, ok := saved.Value.(string)
	if !ok {
		return "", time.Time{}, false
	}

	return serverSlug, saved.Timestamp, true
}
