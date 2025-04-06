package capability

import (
	"sync"
	"time"

	"github.com/gate4ai/mcp/gateway/client"
	"github.com/gate4ai/mcp/server/mcp"
)

// Constants for session parameter keys
const (
	backendSessionsKey = "gw_backend_sessions"
	clientSessionsKey  = "gw_client_sessions"
	serverIDKey        = "gw_server_id"
)

// SavedValue represents a cached value with its timestamp
type SavedValue struct {
	Value     interface{}
	Timestamp time.Time
}

func SaveBackendSessions(sessionParams *sync.Map, clientSessions []*client.Session) {
	sessionParams.Store(backendSessionsKey, &SavedValue{
		Value:     clientSessions,
		Timestamp: time.Now(),
	})
}

// LoadBackendSessions returns backend sessions with timestamp and success indicator
func LoadBackendSessions(sessionParams *sync.Map) ([]*client.Session, time.Time, bool) {
	savedValue, ok1 := sessionParams.Load(backendSessionsKey)
	if !ok1 {
		return []*client.Session{}, time.Time{}, false
	}

	saved, ok2 := savedValue.(*SavedValue)
	if !ok2 {
		return []*client.Session{}, time.Time{}, false
	}

	sessions, ok := saved.Value.([]*client.Session)
	if !ok {
		return []*client.Session{}, time.Time{}, false
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

func SaveServerID(sessionParams *sync.Map, serverID string) {
	sessionParams.Store(serverIDKey, &SavedValue{
		Value:     serverID,
		Timestamp: time.Now(),
	})
}

// GetServerID returns server ID with timestamp and success indicator
func GetServerID(sessionParams *sync.Map) (string, time.Time, bool) {
	savedValue, ok1 := sessionParams.Load(serverIDKey)
	if !ok1 {
		return "", time.Time{}, false
	}

	saved, ok2 := savedValue.(*SavedValue)
	if !ok2 {
		return "", time.Time{}, false
	}

	serverID, ok := saved.Value.(string)
	if !ok {
		return "", time.Time{}, false
	}

	return serverID, saved.Timestamp, true
}
