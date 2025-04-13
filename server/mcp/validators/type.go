package validators

import (
	"fmt"
	"sync"

	"github.com/gate4ai/mcp/shared"
)

// MethodValidator validates that the method in a message exists in the MCP specification
type MethodValidator struct {
	validMethods map[string]bool
	mu           sync.RWMutex
}

// NewMethodValidator creates a new method validator
func NewMethodValidator() *MethodValidator {
	v := &MethodValidator{
		validMethods: map[string]bool{
			// Client Requests
			"initialize":               true,
			"ping":                     true,
			"tools/list":               true,
			"prompts/list":             true,
			"resources/list":           true,
			"resources/templates/list": true,
			"resources/read":           true,
			"resources/subscribe":      true,
			"resources/unsubscribe":    true,
			"prompts/get":              true,
			"tools/call":               true,
			// A2A Methods
			"tasks/send":          true,
			"tasks/sendSubscribe": true,
			"tasks/get":           true,
			"tasks/cancel":        true,
			"logging/setLevel":    true,
			"completion/complete": true,

			// Notifications from the client
			"notifications/initialized":        true,
			"notifications/roots/list_changed": true,
		},
	}

	return v
}

// Validate implements the MessageValidator interface
func (v *MethodValidator) Validate(msg *shared.Message) error {
	if msg.Method != nil {
		v.mu.RLock()
		valid := v.validMethods[*msg.Method]
		v.mu.RUnlock()

		if !valid {
			return fmt.Errorf("invalid method: %s", *msg.Method)
		}
	} else if msg.ID.IsEmpty() {
		return fmt.Errorf("method and id is empty")
	}
	return nil
}
