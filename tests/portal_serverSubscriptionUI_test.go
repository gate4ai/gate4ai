package tests

import (
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionUIVariations(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// --- Test Setup ---
	owner, err := createUser(am)
	require.NoError(t, err, "Failed to create owner user")
	regularUser, err := createUser(am)
	require.NoError(t, err, "Failed to create regular user")

	// Create Public Server
	publicServerSlug := "public-ui-test-" + am.Timestamp
	publicServer, err := addMCPServer(am, owner, publicServerSlug)
	require.NoError(t, err, "Failed to add public server")
	require.NotNil(t, publicServer)
	require.NoError(t, updateServerAvailabilityInDB(t, publicServerSlug, "PUBLIC"), "Failed to set public server availability")

	// Create Subscription Server
	subServerSlug := "sub-ui-test-" + am.Timestamp
	subServer, err := addMCPServer(am, owner, subServerSlug)
	require.NoError(t, err, "Failed to add subscription server")
	require.NotNil(t, subServer)
	// Assuming SUBSCRIPTION is default, or explicitly set:
	// require.NoError(t, updateServerAvailabilityInDB(t, subServerSlug, "SUBSCRIPTION"), "Failed to set subscription server availability")
	// Activate the subscription server so regular user can subscribe
	require.NoError(t, doServerAcvite(am, owner, subServer), "Failed to activate subscription server")

	// Locators
	connInstructionsSelector := ".connection-instructions-card"
	subRequiredAlertSelector := ".v-alert:has-text('Subscription required')"
	subAlertButtonSelector := subRequiredAlertSelector + " button:has-text('Subscribe')"

	// --- Scenario 1: Public Server (Regular User) ---
	t.Run("PublicServer_RegularUser_ShowsInstructions", func(t *testing.T) {
		require.NoError(t, loginUser(am, regularUser.Email, regularUser.Password), "Regular user login failed")
		am.OpenPageWithURL("/servers/" + publicServerSlug)

		// Wait for Instructions
		_, err := am.WaitForLocatorWithDebug(connInstructionsSelector, "public_server_instructions")
		require.NoError(t, err, "Connection Instructions should be visible for public server")

		// Assert Alert NOT Visible
		subAlert := am.Page.Locator(subRequiredAlertSelector)
		require.NoError(t, subAlert.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateHidden,
			Timeout: playwright.Float(1000), // Quick check
		}), "Subscription required alert should NOT be visible for public server")

		am.SaveScreenshot("ui_public_server")
		t.Log("Verified UI for Public Server (Regular User)")
	})

	// --- Scenario 2: Subscription Server (Regular User, Not Subscribed) ---
	t.Run("SubscriptionServer_RegularUser_NotSubscribed_ShowsAlert", func(t *testing.T) {
		require.NoError(t, loginUser(am, regularUser.Email, regularUser.Password), "Regular user login failed")
		am.OpenPageWithURL("/servers/" + subServerSlug)

		// Wait for Alert & Button
		_, err := am.WaitForLocatorWithDebug(subRequiredAlertSelector, "sub_server_alert")
		require.NoError(t, err, "Subscription required alert should be visible")
		_, err = am.WaitForLocatorWithDebug(subAlertButtonSelector, "sub_server_alert_button")
		require.NoError(t, err, "Subscribe button within alert should be visible")

		// Assert Instructions NOT Visible
		instructionsCard := am.Page.Locator(connInstructionsSelector)
		require.NoError(t, instructionsCard.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateHidden,
			Timeout: playwright.Float(1000), // Quick check
		}), "Connection Instructions should NOT be visible for unsubscribed user")

		am.SaveScreenshot("ui_sub_server_not_subscribed")
		t.Log("Verified UI for Subscription Server (Regular User, Not Subscribed)")
	})

	// --- Scenario 3: Subscription Server (Regular User, Subscribed) ---
	t.Run("SubscriptionServer_RegularUser_Subscribed_ShowsInstructions", func(t *testing.T) {
		// Subscribe the user (using existing helper)
		require.NoError(t, subscribeToServer(am, regularUser, subServer), "Failed to subscribe user")

		// Re-navigate or Reload to ensure UI updates
		am.OpenPageWithURL("/servers/" + subServerSlug)
		time.Sleep(500 * time.Millisecond) // Small pause for stability

		// Wait for Instructions
		_, err := am.WaitForLocatorWithDebug(connInstructionsSelector, "sub_server_subscribed_instructions")
		require.NoError(t, err, "Connection Instructions should be visible after subscribing")

		// Assert Alert NOT Visible
		subAlert := am.Page.Locator(subRequiredAlertSelector)
		require.NoError(t, subAlert.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateHidden,
			Timeout: playwright.Float(1000), // Quick check
		}), "Subscription required alert should NOT be visible after subscribing")

		am.SaveScreenshot("ui_sub_server_subscribed")
		t.Log("Verified UI for Subscription Server (Regular User, Subscribed)")
	})

	// --- Scenario 4: Subscription Server (Owner User) ---
	t.Run("SubscriptionServer_Owner_ShowsInstructions", func(t *testing.T) {
		require.NoError(t, loginUser(am, owner.Email, owner.Password), "Owner login failed")
		am.OpenPageWithURL("/servers/" + subServerSlug)

		// Wait for Instructions (Owners have implicit access)
		_, err := am.WaitForLocatorWithDebug(connInstructionsSelector, "sub_server_owner_instructions")
		require.NoError(t, err, "Connection Instructions should be visible for owner")

		// Assert Alert NOT Visible
		subAlert := am.Page.Locator(subRequiredAlertSelector)
		err = subAlert.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateHidden,
			Timeout: playwright.Float(1000), // Quick check
		})
		// Use assert here because the element might just not exist, which is fine
		assert.NoError(t, err, "Subscription required alert should NOT be visible for owner")

		am.SaveScreenshot("ui_sub_server_owner")
		t.Log("Verified UI for Subscription Server (Owner)")
	})
}