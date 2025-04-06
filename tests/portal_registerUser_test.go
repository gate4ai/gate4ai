package tests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
)

// User represents a user in the portal
type User struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"-"` // Not returned from API, but used for registration
	Token    string `json:"token"`
}

// RegisterResponse represents the response from the registration API
type RegisterResponse struct {
	Token string `json:"token"`
	User  struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		Name   string `json:"name"`
		Role   string `json:"role"`
		Status string `json:"status"`
	} `json:"user"`
	Message string `json:"message,omitempty"` // Error message field
}

// createUser creates a new user by registering through the UI
func createUser(am *ArtifactManager) (*User, error) {
	am.T.Helper()

	// Navigate to registration page
	am.OpenPageWithURL("/register")

	// Generate unique credentials for testing
	timestamp := time.Now().UnixNano()
	testUser := &User{
		Name:     fmt.Sprintf("Test User %d", timestamp),
		Email:    fmt.Sprintf("test.user.%d@example.com", timestamp),
		Password: fmt.Sprintf("Password%d!", timestamp),
	}

	// Fill out and submit registration form
	if err := am.Page.Locator("input[type='text']").First().Fill(testUser.Name); err != nil {
		return nil, fmt.Errorf("could not fill name field: %w", err)
	}

	if err := am.Page.Locator("input[type='email']").Fill(testUser.Email); err != nil {
		return nil, fmt.Errorf("could not fill email field: %w", err)
	}

	if err := am.Page.Locator("input[type='password']").First().Fill(testUser.Password); err != nil {
		return nil, fmt.Errorf("could not fill password field: %w", err)
	}

	if err := am.Page.Locator("input[type='password']").Last().Fill(testUser.Password); err != nil {
		return nil, fmt.Errorf("could not fill confirm password field: %w", err)
	}

	if err := am.Page.Locator("input[type='checkbox']").Check(); err != nil {
		return nil, fmt.Errorf("could not check terms checkbox: %w", err)
	}

	// Wait for network responses to capture the registration API call
	responseData := make(chan []byte)
	am.Page.On("response", func(response playwright.Response) {
		go func() {
			if strings.Contains(response.URL(), "/api/auth/register") {
				data, err := response.Body()
				if err == nil {
					responseData <- data
				}
			}
		}()
	})

	// Click the register button
	if err := am.Page.Locator("button[type='submit']").Click(); err != nil {
		return nil, fmt.Errorf("could not click submit button: %w", err)
	}

	err := am.Page.WaitForURL("**/servers")
	if err != nil {
		return nil, fmt.Errorf("navigation did not complete: %w", err)
	}

	// Parse response to extract token and user ID
	datadata := <-responseData
	if len(datadata) > 0 {
		var registerResp RegisterResponse
		if err = json.Unmarshal(datadata, &registerResp); err != nil {
			return nil, fmt.Errorf("failed to parse registration response: %w", err)
		}

		testUser.ID = registerResp.User.ID
		testUser.Token = registerResp.Token
	}

	am.T.Logf("Created user - Email: %s, ID: %s", testUser.Email, testUser.ID)
	return testUser, nil
}

// TestUserRegistration tests the user registration process
func TestUserRegistration(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Verify user was created successfully
	if user.ID == "" || user.Token == "" {
		t.Fatalf("User creation didn't return proper ID or token")
	}

	t.Logf("Successfully created user: %s", user.Email)
}
