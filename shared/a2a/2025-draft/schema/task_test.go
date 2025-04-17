package schema

import (
	"encoding/json"
	"testing"
)

func TestTaskUnmarshal(t *testing.T) {
	// Test case: Unmarshal JSON from the original request (with syntax fixes)
	t.Run("Unmarshal JSON from request", func(t *testing.T) {
		jsonData := `{
			"id": "1",
			"sessionId": "2",
			"status": {
				"state": "failed",
				"timestamp": "2025-04-17T10:34:18.117Z",
				"message": {
					"role": "agent",
					"parts": [{"text": "No type"}]
				}
			},
			"artifacts": []
		}`

		var task Task
		err := json.Unmarshal([]byte(jsonData), &task)
		if err != nil {
			t.Fatalf("Failed to unmarshal Task JSON: %v", err)
		}

		// Very basic checks to confirm successful unmarshal
		if task.ID != "1" {
			t.Errorf("Expected task ID '1', got '%s'", task.ID)
		}

		if task.Status.State != TaskStateFailed {
			t.Errorf("Expected status 'failed', got '%s'", task.Status.State)
		}
	})
}
