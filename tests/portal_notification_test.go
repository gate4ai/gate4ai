package tests

import (
	"database/sql"
	"fmt"
	"os" // Import os package to read environment variable
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/tests/env"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlobalNotificationBar tests the visibility and content of the notification bar
func TestGlobalNotificationBarWithEnv(t *testing.T) { // Renamed test function slightly
	// --- Test Setup ---
	// Read the *static* notification from the environment variable *once*
	staticTestMessage := os.Getenv("NUXT_GATE4AI_NOTIFICATION")
	if staticTestMessage == "" {
		t.Log("NUXT_GATE4AI_NOTIFICATION env var not set, running tests without static message.")
	} else {
		t.Logf("Running tests with static message from env: '%s'", staticTestMessage)
	}

	am := NewArtifactManager(t)
	defer am.Close()

	// Helpers for selecting notification elements using data-testid
	notificationBarSelector := "[data-testid='global-notification-bar']"
	notificationTextSelector := "[data-testid='global-notification-text']"

	// Create a user to interact with the portal
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")
	require.NoError(t, loginUser(am, user.Email, user.Password), "Login failed")

	// --- Test Case 1: No Dynamic Message ---
	t.Run("NoDynamicMessage", func(t *testing.T) {
		// Ensure dynamic message is empty in DB
		require.NoError(t, updateSettingInDB(t, "general_notification_dynamic", ""), "Failed to clear dynamic notification")
		// Reload page to fetch settings
		_, err = am.Page.Reload(playwright.PageReloadOptions{WaitUntil: playwright.WaitUntilStateNetworkidle})
		require.NoError(t, err, "Failed to reload page")
		time.Sleep(500 * time.Millisecond) // Short pause for UI update

		notificationBar := am.Page.Locator(notificationBarSelector)
		notificationText := notificationBar.Locator(notificationTextSelector) // Locate text within the bar

		if staticTestMessage == "" {
			// Expect bar to be hidden if static env var is also empty
			assert.NoError(t, notificationBar.WaitFor(playwright.LocatorWaitForOptions{
				State:   playwright.WaitForSelectorStateHidden,
				Timeout: playwright.Float(2000),
			}), "Notification bar should be hidden when both env and DB messages are empty")
			t.Log("Verified: Bar hidden (no messages)")
		} else {
			// Expect bar with only the static message from env var
			assert.NoError(t, notificationBar.WaitFor(playwright.LocatorWaitForOptions{
				State:   playwright.WaitForSelectorStateVisible,
				Timeout: playwright.Float(5000),
			}), "Notification bar should be visible (static message only)")
			text, err := notificationText.TextContent()
			require.NoError(t, err, "Failed to get notification text")
			assert.Equal(t, staticTestMessage, text, "Notification text should match static env message")
			t.Logf("Verified: Bar shows static message: '%s'", text)
		}
		am.SaveScreenshot("notification_no_dynamic")
	})

	// --- Test Case 2: Dynamic Message Only (Static message might still be present from Env) ---
	t.Run("DynamicMessageSet", func(t *testing.T) {
		dynamicMessage := "Scheduled Maintenance Tonight"
		// Set dynamic message in DB
		require.NoError(t, updateSettingInDB(t, "general_notification_dynamic", dynamicMessage), "Failed to set dynamic notification")
		// Reload page
		_, err = am.Page.Reload(playwright.PageReloadOptions{WaitUntil: playwright.WaitUntilStateNetworkidle})
		require.NoError(t, err, "Failed to reload page")
		time.Sleep(500 * time.Millisecond) // Short pause

		notificationBar := am.Page.Locator(notificationBarSelector)
		notificationText := notificationBar.Locator(notificationTextSelector)

		// Expect bar to be visible because dynamic message is set
		require.NoError(t, notificationBar.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateVisible,
			Timeout: playwright.Float(5000),
		}), "Notification bar should be visible (dynamic message set)")

		text, err := notificationText.TextContent()
		require.NoError(t, err, "Failed to get notification text")

		// Determine expected combined message
		expectedMessage := dynamicMessage // Assume only dynamic first
		if staticTestMessage != "" {
			expectedMessage = fmt.Sprintf("%s | %s", staticTestMessage, dynamicMessage) // Combine if static exists
		}

		assert.Equal(t, expectedMessage, text, "Notification text mismatch (dynamic/combined)")
		t.Logf("Verified: Bar shows message: '%s'", text)
		am.SaveScreenshot("notification_dynamic_set")
	})

	// --- Test Case 3: Update Dynamic Message ---
	t.Run("UpdateDynamicMessage", func(t *testing.T) {
		newDynamicMessage := "Maintenance Extended"
		// Update dynamic message in DB
		require.NoError(t, updateSettingInDB(t, "general_notification_dynamic", newDynamicMessage), "Failed to update dynamic notification")
		// Reload page
		_, err = am.Page.Reload(playwright.PageReloadOptions{WaitUntil: playwright.WaitUntilStateNetworkidle})
		require.NoError(t, err, "Failed to reload page")
		time.Sleep(500 * time.Millisecond) // Short pause

		notificationBar := am.Page.Locator(notificationBarSelector)
		notificationText := notificationBar.Locator(notificationTextSelector)

		// Expect bar to be visible
		require.NoError(t, notificationBar.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateVisible,
			Timeout: playwright.Float(5000),
		}), "Notification bar should be visible (dynamic message updated)")

		text, err := notificationText.TextContent()
		require.NoError(t, err, "Failed to get notification text")

		// Determine expected combined message
		expectedMessage := newDynamicMessage // Assume only dynamic first
		if staticTestMessage != "" {
			expectedMessage = fmt.Sprintf("%s | %s", staticTestMessage, newDynamicMessage) // Combine if static exists
		}

		assert.Equal(t, expectedMessage, text, "Notification text did not update correctly")
		t.Logf("Verified: Bar shows updated message: '%s'", text)
		am.SaveScreenshot("notification_dynamic_updated")
	})

	// --- Test Case 4: Clear Dynamic Message ---
	t.Run("ClearDynamicMessage", func(t *testing.T) {
		// Clear dynamic message in DB
		require.NoError(t, updateSettingInDB(t, "general_notification_dynamic", ""), "Failed to clear dynamic notification again")
		// Reload page
		_, err = am.Page.Reload(playwright.PageReloadOptions{WaitUntil: playwright.WaitUntilStateNetworkidle})
		require.NoError(t, err, "Failed to reload page")
		time.Sleep(500 * time.Millisecond) // Short pause

		notificationBar := am.Page.Locator(notificationBarSelector)
		notificationText := notificationBar.Locator(notificationTextSelector)

		if staticTestMessage == "" {
			// Expect bar to be hidden if static env var is also empty
			assert.NoError(t, notificationBar.WaitFor(playwright.LocatorWaitForOptions{
				State:   playwright.WaitForSelectorStateHidden,
				Timeout: playwright.Float(2000),
			}), "Notification bar should be hidden after clearing dynamic message (static also empty)")
			t.Log("Verified: Bar hidden (dynamic cleared, static empty)")
		} else {
			// Expect bar with only the static message from env var again
			require.NoError(t, notificationBar.WaitFor(playwright.LocatorWaitForOptions{
				State:   playwright.WaitForSelectorStateVisible,
				Timeout: playwright.Float(5000),
			}), "Notification bar should be visible (dynamic cleared, static exists)")
			text, err := notificationText.TextContent()
			require.NoError(t, err, "Failed to get notification text")
			assert.Equal(t, staticTestMessage, text, "Notification text should revert to static env message")
			t.Logf("Verified: Bar shows static message again: '%s'", text)
		}
		am.SaveScreenshot("notification_dynamic_cleared")
	})

	// --- Final Cleanup: Clear dynamic setting ---
	t.Log("Cleaning up dynamic notification setting...")
	require.NoError(t, updateSettingInDB(t, "general_notification_dynamic", ""), "Failed to clear dynamic notification post-test")
}

func updateSettingInDB(t *testing.T, key, value string) error {
	db, err := sql.Open("postgres", env.GetURL(env.DBComponentName))
	if err != nil {
		return err
	}
	return env.UpdateSettingInDB(t.Context(), db, key, value)
}
