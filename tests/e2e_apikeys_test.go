package tests

import (
	"testing"

	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/stretchr/testify/require"
)

// TestGatewayAPIKeyAuthorization tests API key authorization in the gateway
func TestGatewayAPIKeyAuthorization(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	owner, err := createUser(am)
	require.NoError(t, err, "Failed to create owner user")
	t.Logf("Owner user created: %s", owner.Email)

	subscriber, err := createUser(am)
	require.NoError(t, err, "Failed to create subscriber user")
	t.Logf("Subscriber user created: %s", subscriber.Email)

	nonSubscriber, err := createUser(am)
	require.NoError(t, err, "Failed to create non-subscriber user")
	t.Logf("Non-subscriber user created: %s", nonSubscriber.Email)

	// 1. Owner adds a demo server
	t.Log("Owner adding server...")
	server, err := addMCPServer(am, owner, "test-gateway-apikey-authorization")
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)
	t.Logf("Server added by owner. Slug: %s", server.Slug)

	// 2. Subscriber subscribes to the server
	t.Logf("Subscriber (%s) subscribing to server (%s)...", subscriber.Email, server.Slug)
	err = subscribeToServer(am, subscriber, server)
	require.NoError(t, err, "Failed to subscribe to server")
	t.Log("Subscriber successfully subscribed.")

	// 3. Create API keys for all users
	t.Log("Creating API keys...")
	ownerKey, err := createAPIKey(am, owner)
	require.NoError(t, err, "Failed to create API key for owner")
	require.NotEmpty(t, ownerKey.Key, "Owner API key is empty")
	t.Logf("Owner API key created: %s...", ownerKey.Key[:8])

	subscriberKey, err := createAPIKey(am, subscriber)
	require.NoError(t, err, "Failed to create API key for subscriber")
	require.NotEmpty(t, subscriberKey.Key, "Subscriber API key is empty")
	t.Logf("Subscriber API key created: %s...", subscriberKey.Key[:8])

	nonSubscriberKey, err := createAPIKey(am, nonSubscriber)
	require.NoError(t, err, "Failed to create API key for non-subscriber")
	require.NotEmpty(t, nonSubscriberKey.Key, "Non-subscriber API key is empty")
	t.Logf("Non-subscriber API key created: %s...", nonSubscriberKey.Key[:8])

	// 4. Make gateway API calls with each key
	// Gateway uses V2025 endpoint /mcp
	FULL_GATEWAY_URL := GATEWAY_URL + transport.MCP2024_PATH

	// --- Test Owner Key ---
	t.Logf("Testing owner API key (%s...)", ownerKey.Key[:8])
	list, err := GetToolsList(FULL_GATEWAY_URL, ownerKey.Key, am.Logger)
	// Owner bypasses server status and subscription checks in the gateway's perspective
	// They always see the tools of the servers they own, even if DRAFT.
	require.NoError(t, err, "Failed to get owner tools list")
	t.Logf("Owner tools list (server DRAFT): %v", list)
	// Example server has 6 tools defined in startExample.go
	require.Len(t, list, 6, "Owner API request returned incorrect number of tools (server DRAFT)")
	t.Log("Owner key test passed (server DRAFT).")

	// --- Test Subscriber Key (Server DRAFT) ---
	t.Logf("Testing subscriber API key (%s...) (server DRAFT)", subscriberKey.Key[:8])
	list, err = GetToolsList(FULL_GATEWAY_URL, subscriberKey.Key, am.Logger)
	require.NoError(t, err, "Failed to get subscriber tools list (server DRAFT)")
	t.Logf("Subscriber tools list (server DRAFT): %v", list)
	// Subscriber should not see tools if the server is not ACTIVE
	require.Len(t, list, 0, "Subscriber API request returned tools for non-active server")
	t.Log("Subscriber key test passed (server DRAFT).")

	// --- Activate Server ---
	t.Logf("Owner (%s) activating server (%s)...", owner.Email, server.Slug)
	err = doServerAcvite(am, owner, server)
	require.NoError(t, err, "Failed to activate server")
	t.Log("Server activated.")

	// --- Test Subscriber Key (Server ACTIVE) ---
	t.Logf("Testing subscriber API key (%s...) (server ACTIVE)", subscriberKey.Key[:8])
	list, err = GetToolsList(FULL_GATEWAY_URL, subscriberKey.Key, am.Logger)
	require.NoError(t, err, "Failed to get subscriber tools list (server ACTIVE)")
	t.Logf("Subscriber tools list (server ACTIVE): %v", list)
	require.Len(t, list, 6, "Subscriber API request returned incorrect number of tools (server ACTIVE)")
	t.Log("Subscriber key test passed (server ACTIVE).")

	// --- Test Non-Subscriber Key (Server ACTIVE) ---
	t.Logf("Testing non-subscriber API key (%s...) (server ACTIVE)", nonSubscriberKey.Key[:8])
	list, err = GetToolsList(FULL_GATEWAY_URL, nonSubscriberKey.Key, am.Logger)
	require.NoError(t, err, "Failed to get non-subscriber tools list (server ACTIVE)")
	t.Logf("Non-subscriber tools list (server ACTIVE): %v", list)
	require.Len(t, list, 0, "Non-subscriber API request returned tools (server ACTIVE)")
	t.Log("Non-subscriber key test passed (server ACTIVE).")

	// --- Test Invalid Key ---
	t.Logf("Testing invalid API key")
	_, err = GetToolsList(FULL_GATEWAY_URL, "invalid-key-does-not-exist", am.Logger)
	// Expect an error because the key is invalid
	require.Error(t, err, "Expected an error when using an invalid API key")
	require.Contains(t, err.Error(), "failed to initialize tools", "Error message should indicate invalid token/key")
	t.Logf("Invalid key test passed (received expected error: %v)", err)
}
