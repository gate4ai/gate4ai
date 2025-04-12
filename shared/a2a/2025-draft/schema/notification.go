package schema

// AuthenticationInfo provides details needed for the agent to authenticate when calling a push notification URL.
// Note: This structure is identical to AgentAuthentication in the provided schema, potentially indicating reuse.
// If they can diverge, define separately. Using AgentAuthentication for now based on schema similarity.
type AuthenticationInfo = AgentAuthentication // Reusing AgentAuthentication structure

// PushNotificationConfig defines the configuration for push notifications to a client endpoint.
type PushNotificationConfig struct {
	// The URL endpoint where the agent should POST notifications.
	URL string `json:"url"`
	// An optional bearer token the agent must include in the Authorization header when posting.
	Token *string `json:"token,omitempty"`
	// Authentication details the agent needs to use to call the notification URL.
	Authentication *AuthenticationInfo `json:"authentication,omitempty"`
}

// TaskPushNotificationConfig associates a push notification configuration with a specific task.
type TaskPushNotificationConfig struct {
	// The ID of the task this configuration applies to.
	ID string `json:"id"`
	// The push notification configuration details.
	PushNotificationConfig PushNotificationConfig `json:"pushNotificationConfig"`
}
