package tests

import (
	"errors"
	"strings"
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

	// Wait for either error message or successful navigation
	for time.Now().Before(time.Now().Add(10 * time.Second)) {
		// Check for error message in snackbar
		errorLocator := am.Page.Locator(".v-snackbar__content:has-text('Invalid email or password')")
		isVisible, err := errorLocator.IsVisible()
		if err == nil && isVisible {
			am.SaveScreenshot("login_invalid_credentials")
			return &errorWithContext{message: "Invalid email or password", context: "login_credentials_error"}
		}

		// Check if we've reached the servers page
		currentURL := am.Page.URL()
		if strings.Contains(currentURL, "/servers") {
			am.T.Logf("Successfully logged in user: %s", email)
			return nil
		}

		// Wait a bit before next check
		time.Sleep(100 * time.Millisecond)
	}

	// If we got here, neither condition was met within the timeout
	am.SaveScreenshot("login_timeout")
	return errors.New("login timeout: neither error message nor successful navigation occurred within 10 seconds")
}

// errorWithContext is a custom error type that includes context information
type errorWithContext struct {
	message string
	context string
}

// Error implements the error interface
func (e *errorWithContext) Error() string {
	return e.message
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
