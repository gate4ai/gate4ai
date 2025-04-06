package tests

import (
	"fmt"
	"testing"

	"github.com/playwright-community/playwright-go"
)

// navigatePages tests basic navigation between pages after login
func navigatePages(am *ArtifactManager, user *User) error {
	// Navigate to login page
	loginUser(am, user.Email, user.Password)

	// Wait for successful login by checking for servers page load
	if err := am.Page.Locator("h1:has-text('Server Catalog')").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		am.SaveScreenshot("login_navigation_failed")
		return fmt.Errorf("login failed or navigation after login failed: %w", err)
	}

	am.T.Logf("Successfully logged in user: %s", user.Email)

	// Test navigation to different pages
	pages := []struct {
		name string
		path string
	}{
		{"Home", "/"},
		{"Servers", "/servers"},
		{"Login", "/login"},
		{"Register", "/register"},
		{"Privacy", "/privacy"},
		{"Profile", "/profile"},
		{"Keys", "/keys"},
	}

	for _, p := range pages {
		am.T.Logf("Navigating to %s page", p.name)
		am.OpenPageWithURL(p.path)

		// Take a screenshot
		screenshotName := fmt.Sprintf("page_%s", p.name)
		am.SaveScreenshot(screenshotName)
		am.T.Logf("Screenshot saved for %s", p.name)
	}

	return nil
}

// TestBasicNavigation tests basic navigation between pages
func TestBasicNavigation(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Test navigation to various pages
	err = navigatePages(am, user)
	if err != nil {
		t.Fatalf("Failed to navigate pages: %v", err)
	}

	t.Logf("Successfully navigated through pages")
}
