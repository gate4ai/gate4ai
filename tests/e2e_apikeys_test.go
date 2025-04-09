package tests

import (
	"testing"
)

// TestGatewayAPIKeyAuthorization tests API key authorization in the gateway
func TestGatewayAPIKeyAuthorization(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	owner, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create owner user: %v", err)
	}

	subscriber, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create subscriber user: %v", err)
	}

	// Create non-subscriber user
	nonSubscriber, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create non-subscriber user: %v", err)
	}

	// 1. Owner adds a demo server
	server, err := addServer(am, owner)
	if err != nil {
		t.Fatalf("Failed to add server: %v", err)
	}

	// 2. Subscriber subscribes to the server
	err = subscribeToServer(am, subscriber, server)
	if err != nil {
		t.Fatalf("Failed to subscribe to server: %v", err)
	}

	// 3. Create API keys for all users
	ownerKey, err := createAPIKey(am, owner)
	if err != nil {
		t.Fatalf("Failed to create API key for owner: %v", err)
	}

	subscriberKey, err := createAPIKey(am, subscriber)
	if err != nil {
		t.Fatalf("Failed to create API key for subscriber: %v", err)
	}

	nonSubscriberKey, err := createAPIKey(am, nonSubscriber)
	if err != nil {
		t.Fatalf("Failed to create API key for non-subscriber: %v", err)
	}

	// 4. Make demo server API calls with each key
	// Get URL for the demo API
	FULL_GATEWAY_URL := GATEWAY_URL + "/sse"

	// Try owner key
	t.Logf("Testing owner API key")
	list, err := GetToolsList(FULL_GATEWAY_URL, ownerKey.Key, am.Logger)
	if err != nil {
		t.Fatalf("Failed to get owner tools list: %v", err)
	}
	t.Logf("owner tools list: %v", list)
	if len(list) != 6 {
		t.Fatalf("Owner API request returned %d tools, expected 6", len(list))
	}

	// Try subscriber key
	t.Logf("Testing subscriber API key (server is not active)")
	list, err = GetToolsList(FULL_GATEWAY_URL, subscriberKey.Key, am.Logger)
	if err != nil {
		t.Fatalf("Failed to get subscriber tools list (server is not active): %v", err)
	}
	t.Logf("subscriber tools list: %v", list)
	if len(list) != 0 {
		t.Fatalf("subscriber API request (server is not active) returned %d tools, expected 0", len(list))
	}

	err = doServerAcvite(am, owner, server)
	if err != nil {
		t.Fatalf("Failed to activate server: %v", err)
	}

	// Try subscriber key
	t.Logf("Testing subscriber (server is actived) API key")
	list, err = GetToolsList(FULL_GATEWAY_URL, subscriberKey.Key, am.Logger)
	if err != nil {
		t.Fatalf("Failed to get subscriber (server is actived) tools list: %v", err)
	}
	t.Logf("owner tools list: %v", list)
	if len(list) != 6 {
		t.Fatalf("Owner API request returned %d tools (server is actived), expected 6", len(list))
	}

	// Try non-subscriber key
	t.Logf("Testing unsubscriber (server is actived) API key")
	list, err = GetToolsList(FULL_GATEWAY_URL, nonSubscriberKey.Key, am.Logger)
	if err != nil {
		t.Fatalf("Failed to get nonSubscriberKey tools list: %v", err)
	}
	t.Logf("owner tools list: %v", list)
	if len(list) != 0 {
		t.Fatalf("nonSubscriberKey API request returned %d tools (server is actived), expected 0", len(list))
	}

	// Test with invalid key - should fail with 401 Unauthorized
	t.Logf("Testing invalid API key")
	list, err = GetToolsList(FULL_GATEWAY_URL, "invalid-key", am.Logger)
	if err == nil {
		t.Fatalf("Failed to get invalid key tools list: %v", list)
	}
}
