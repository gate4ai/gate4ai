package validators

import (
	"errors"
	"sync"

	"github.com/gate4ai/mcp/shared"
)

// MessageSizeValidator validates the size of incoming messages
type MessageSizeValidator struct {
	maxSize int64
	mu      sync.RWMutex
}

// NewMessageSizeValidator creates a new message size validator
func NewMessageSizeValidator(maxSize int64) *MessageSizeValidator {
	return &MessageSizeValidator{
		maxSize: maxSize,
	}
}

// SetMaxSize updates the maximum allowed message size
func (v *MessageSizeValidator) SetMaxSize(maxSize int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.maxSize = maxSize
}

// Validate implements the MessageValidator interface
func (v *MessageSizeValidator) Validate(msg *shared.Message) error {
	if len(msg.ID.String()) >= 256 {
		return errors.New("message ID string exceeds maximum allowed length (256 bytes)")
	}

	// Validate Params size
	// If there are no parameters, there's nothing to validate
	if msg.Params == nil {
		return nil
	}

	v.mu.RLock()
	maxSize := v.maxSize
	v.mu.RUnlock()

	if int64(len(*msg.Params)) > maxSize {
		return errors.New("message exceeds maximum allowed size")
	}
	//TODO: Validate error, result, etc size

	return nil
}
