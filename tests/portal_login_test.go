package tests

import (
	"testing"
	"time"
)

// loginUser logs in an existing user using Playwright browser automation
func loginUser(am *ArtifactManager, email, password string) error {
	// Navigate to login page
	am.OpenPageWithURL("/login")

	time.Sleep(100 * time.Millisecond) // sometimes the email address was not filled in

	// Fill out the login form with credentials
	if err := am.Page.Locator("input[type='email']").Fill(email); err != nil {
		am.SaveScreenshot("login_email_error")
		return err
	}

	if err := am.Page.Locator("input[type='password']").Fill(password); err != nil {
		am.SaveScreenshot("login_password_error")
		return err
	}

	// Click the login button
	if err := am.Page.Locator("button[type='submit']").Click(); err != nil {
		am.SaveScreenshot("login_button_error")
		return err
	}

	// Wait for successful login by checking for servers page load
	err := am.Page.WaitForURL("**/servers")
	if err != nil {
		am.SaveScreenshot("login_navigation_error")
		return err
	}

	am.T.Logf("Successfully logged in user: %s", email)
	return nil
}

// TestLogin tests user login
func TestLogin(t *testing.T) {
	// Create artifact manager
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Now, log in with the created user
	err = loginUser(am, user.Email, user.Password)
	if err != nil {
		t.Fatalf("Failed to login: %v", err)
	}

	t.Logf("Successfully logged in as user: %s", user.Email)
}
