package tests

import (
	"fmt"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// subscribeToServer subscribes a user to a server using Playwright
func subscribeToServer(am *ArtifactManager, user *User, server *CatalogServer) error {
	if err := loginUser(am, user.Email, user.Password); err != nil {
		return fmt.Errorf("login failed before subscribing to server: %w", err)
	}
	// Navigate to the server details page using SLUG
	serverDetailURL := fmt.Sprintf("/servers/%s", server.Slug)
	am.OpenPageWithURL(serverDetailURL)

	// Take a screenshot of the server details page
	am.SaveScreenshot("server_details_for_subscription")

	// Find and click the Subscribe button
	// Use a more specific selector if needed, e.g., targeting within ServerInfo component
	subscribeBtnSelector := "button:has(span.v-btn__content > i.mdi-account-plus):has-text('Subscribe')"
	subscribeBtn, err := am.WaitForLocatorWithDebug(subscribeBtnSelector, "subscribe_button")
	if err != nil {
		// Try alternative selector if the first fails
		subscribeBtnAltSelector := "button:has-text('Subscribe')"
		subscribeBtn, err = am.WaitForLocatorWithDebug(subscribeBtnAltSelector, "subscribe_button_alt")
		if err != nil {
			return fmt.Errorf("failed to find subscribe button using multiple selectors: %w", err)
		}
	}

	if err = subscribeBtn.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)}); err != nil {
		am.SaveLocatorDebugInfo(subscribeBtnSelector, "subscribe_button_click_failed")
		return fmt.Errorf("failed to click subscribe button: %w", err)
	}

	// Wait for subscription to complete - look for success notification or state change
	// Look for the button changing to "Unsubscribe"
	unsubscribeBtnSelector := "button:has(span.v-btn__content > i.mdi-account-minus):has-text('Unsubscribe')"
	_, err = am.WaitForLocatorWithDebug(unsubscribeBtnSelector, "unsubscribe_button_visible_after_subscribe")
	if err != nil {
		// Also check for success snackbar as a fallback confirmation
		if _, errSnack := am.WaitForLocatorWithDebug(".v-snackbar:has-text('Successfully subscribed!')", "subscribe_success_snackbar"); errSnack != nil {
			am.SaveScreenshot("subscription_confirmation_failed")
			// Return the original error about the button not changing
			return fmt.Errorf("subscription confirmation failed: Unsubscribe button not found and no success snackbar: %w", err)
		}
		am.T.Logf("Warning: Unsubscribe button not found, but success snackbar appeared.")
	}

	// Take a screenshot after successful subscription
	am.SaveScreenshot("after_server_subscription")
	am.T.Logf("User %s successfully subscribed to server %s", user.Email, server.Slug)
	return nil
}

// TestServerSubscriptions tests server subscription functionality
func TestServerSubscriptions(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	owner, err := createUser(am)
	require.NoError(t, err, "Failed to create owner user")

	subscriber, err := createUser(am)
	require.NoError(t, err, "Failed to create subscriber user")

	// Owner adds the server
	server, err := addServer(am, owner, "test-server-subscriptions")
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)

	t.Logf("Activating server %s before subscription test", server.Slug)
	err = doServerAcvite(am, owner, server)
	require.NoError(t, err, "Failed to activate server")

	// Subscriber subscribes to the (now active) server
	err = subscribeToServer(am, subscriber, server)
	require.NoError(t, err, "Failed to subscribe to server")
}
