package validators

import (
	"github.com/gate4ai/mcp/shared"
)

// CreateDefaultValidators returns the standard set of validators with default settings
func CreateDefaultValidators() []shared.MessageValidator {
	return []shared.MessageValidator{
		NewThrottling(60, 600),          // 60 requests per second, 600 requests per minute
		NewMessageSizeValidator(102400), //100KB
		NewMethodValidator(),
	}
}
