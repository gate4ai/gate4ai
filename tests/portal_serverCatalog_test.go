package tests

import (
	"testing"
	"time"
)

// browseServerCatalog navigates to the server catalog page and verifies it loads correctly
func browseServerCatalog(am *ArtifactManager, user *User) error {

	// First, log in the user
	loginUser(am, user.Email, user.Password)

	// Take a screenshot after login
	am.SaveScreenshot("after_login")

	// Wait for successful login by checking for servers page load
	if _, err := am.WaitForLocatorWithDebug("h1:has-text('Server Catalog')", "server_catalog_heading"); err != nil {
		am.T.Fatalf("server catalog page did not load: %v", err)
		return nil
	}

	// Take a screenshot of the server catalog page
	am.SaveScreenshot("server_catalog_page")

	// Verify search functionality is present using multiple approaches
	// Try various selector strategies in order of preference
	searchSelectors := []struct {
		selector    string
		description string
	}{
		// Try label-based selector first (most reliable)
		{`input[label*="Search servers"]`, "label_based_search"},
		// Try placeholder-based selector as second option
		{`input[placeholder*="Search servers"]`, "placeholder_search"},
		// Try common input field classes as fallback
		{".v-field__input", "class_based_search"},
		// Final fallback for any input that might be a search
		{"input.v-text-field__input", "generic_input"},
	}

	searchInputFound := false
	for _, s := range searchSelectors {
		locator := am.Page.Locator(s.selector)
		visible, err := locator.IsVisible()
		if err == nil && visible {
			am.T.Logf("Found search input using selector: %s", s.selector)
			searchInputFound = true
			break
		}
	}

	if !searchInputFound {
		// Take debug screenshot and save HTML
		am.SaveScreenshot("search_input_not_found")
		am.SaveHTML("search_input_not_found")
		am.T.Fatalf("search input not found using any of the selectors")
		return nil
	}

	// Wait for page to stabilize - either server cards are loaded or empty state is displayed
	// First, get the locators for both possible states
	serverCardLocator := am.Page.Locator("div .v-card").First()
	emptyStateLocator := am.Page.Locator("text='No servers found'")

	// Maximum wait time for either state to appear
	const maxWaitTime = 30000
	startTime := time.Now()

	var serverCardsVisible, emptyStateVisible bool

	// Poll until one of the conditions is met or timeout occurs
	for time.Since(startTime).Milliseconds() < maxWaitTime {
		// Check if server cards are visible
		serverCardsVisible, _ = serverCardLocator.IsVisible()

		// Check if empty state is visible
		emptyStateVisible, _ = emptyStateLocator.IsVisible()

		// If either condition is met, exit the loop
		if serverCardsVisible || emptyStateVisible {
			break
		}

		// Small delay before checking again
		time.Sleep(100 * time.Millisecond)
	}

	// Check if either condition was met
	if !serverCardsVisible && !emptyStateVisible {
		am.SaveLocatorDebugInfo("v-card, text='No servers found'", "neither_condition_met")
		am.T.Fatalf("neither server cards nor empty state message appeared after %d milliseconds", maxWaitTime)
		return nil
	}

	// Process based on which condition was met
	if serverCardsVisible {
		// Server cards are visible, take a screenshot and count them
		am.SaveScreenshot("server_cards")

		// Now it's safe to count the cards
		serverCardsCount, err := am.Page.Locator("v-card").Count()
		if err != nil {
			am.SaveLocatorDebugInfo("v-card", "server_cards_count_failed")
			am.T.Fatalf("could not count server cards: %v", err)
			return nil
		}

		am.T.Logf("Found %d server cards", serverCardsCount)
	} else {
		// Empty state is visible, take a screenshot
		am.SaveScreenshot("empty_state")
		am.T.Logf("Found empty state message (no servers available)")
	}

	return nil
}

// TestServerCatalog tests browsing the server catalog
func TestServerCatalog(t *testing.T) {
	// Create artifact manager
	am := NewArtifactManager(t)
	defer am.Close()

	am.T.Logf("Creating user")
	user, err := createUser(am)
	if err != nil {
		am.T.Fatalf("Failed to create user: %v", err)
	}

	am.T.Logf("Adding servers")
	addMCPServer(am, user, "test-server-catalog1")
	addMCPServer(am, user, "test-server-catalog2")

	// Browse the server catalog
	err = browseServerCatalog(am, user)
	if err != nil {
		am.T.Fatalf("Failed to browse server catalog: %v", err)
	}
	am.T.Logf("Successfully browsed server catalog")
}
