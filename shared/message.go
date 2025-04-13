package shared

import (
	"encoding/json"
	"fmt"
	"time"

	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
)

type Message struct {
	ID        *schema.RequestID `json:"id,omitempty"`
	Timestamp time.Time         `json:"-"`
	Method    *string           `json:"method,omitempty"`
	Params    *json.RawMessage  `json:"params,omitempty"`
	Result    *json.RawMessage  `json:"result,omitempty"`
	Error     *JSONRPCError     `json:"error,omitempty"`
	A2AEvent  *A2AStreamEvent   `json:"-"` // Field to hold A2A SSE event data if applicable

	Processed bool     `json:"-"`
	Session   ISession `json:"-"` // Will be either client.Session or mcp.Session
}

func ParseMessages(s ISession, data []byte) ([]*Message, error) {
	var messages []*Message
	err := json.Unmarshal(data, &messages)
	if err == nil {
		for _, msg := range messages {
			if msg != nil {
				msg.Session = s
			}
		}
		return messages, nil
	}

	var singleMessage Message
	err = json.Unmarshal(data, &singleMessage)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON-RPC message (neither batch nor single): %w", err)
	}
	singleMessage.Session = s
	return []*Message{&singleMessage}, nil
}

// A2AStreamEvent holds data for A2A SSE events, used internally by transport
type A2AStreamEvent struct {
	Type     string // "status" or "artifact"
	Status   *a2aSchema.TaskStatusUpdateEvent
	Artifact *a2aSchema.TaskArtifactUpdateEvent
	Final    bool
}

// NilIfNil returns "nil" if the string pointer is nil, otherwise returns the pointed-to string.
func NilIfNil(s *string) string {
	if s == nil {
		return "nil"
	}
	return *s
}

// MarshalJSON ensures the JSONRPC field is properly set before marshaling
func (m *Message) MarshalJSON() ([]byte, error) {
	if m.Error != nil {
		response := JSONRPCErrorResponse{
			JSONRPC: "2.0",
			ID:      m.ID,
			Error:   m.Error,
		}
		return json.Marshal(response)
	}
	if m.Result != nil {
		response := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      m.ID,
			Result:  m.Result,
		}
		return json.Marshal(response)
	}
	response := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      m.ID,
		Method:  m.Method,
		Params:  m.Params,
	}
	return json.Marshal(response)
}
