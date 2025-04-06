package tests

import (
	"fmt"
	"testing"
)

// subscribeToServer subscribes a user to a server using Playwright
func subscribeToServer(am *ArtifactManager, user *User, server *CatalogServer) error {
	err := loginUser(am, user.Email, user.Password)
	if err != nil {
		return fmt.Errorf("login failed before subscribing to server: %w", err)
	}
	// Navigate to the server details page
	serverDetailURL := fmt.Sprintf("/servers/%s", server.ID)
	am.OpenPageWithURL(serverDetailURL)

	// Take a screenshot of the server details page
	am.SaveScreenshot("server_details_for_subscription")

	// Find and click the Subscribe button
	subscribeBtn, err := am.WaitForLocatorWithDebug("button:has-text('Subscribe')", "subscribe_button")
	if err != nil {
		return fmt.Errorf("failed to find subscribe button: %w", err)
	}

	err = subscribeBtn.Click()
	if err != nil {
		return fmt.Errorf("failed to click subscribe button: %w", err)
	}

	// Wait for subscription to complete - look for success notification or state change
	// This could be a snackbar message or button text change to "Unsubscribe"
	_, err = am.WaitForLocatorWithDebug("button:has-text('Unsubscribe')", "unsubscribe_button")
	if err != nil {
		return fmt.Errorf("subscription was not confirmed: %w", err)
	}

	// Take a screenshot after successful subscription
	am.SaveScreenshot("after_server_subscription")

	return nil
}

// TestServerSubscriptions tests server subscription functionality
func TestServerSubscriptions(t *testing.T) {
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

	server, err := addServer(am, owner)
	if err != nil {
		t.Fatalf("Failed to add server: %v", err)
	}

	err = subscribeToServer(am, subscriber, server)
	if err != nil {
		t.Fatalf("Failed to subscribe to server: %v", err)
	}
}
