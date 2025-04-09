package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
)

// editUserProfile edits the profile of a logged-in user using Playwright
func editUserProfile(am *ArtifactManager, user *User, newName, newCompany string) error {
	loginUser(am, user.Email, user.Password)

	// Navigate to the profile page
	am.OpenPageWithURL("/profile")

	// Wait for the profile form to load
	profileForm := am.Page.Locator("form.v-form").First()
	if err := profileForm.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		am.SaveScreenshot("profile_page_not_loaded")
		return fmt.Errorf("profile page did not load correctly: %w", err)
	}

	// Clear and update the name field - using input with Full Name label
	nameField := am.Page.Locator("input[id='input-2']")
	if err := nameField.Clear(); err != nil {
		// Try alternative selector if the id-based one fails
		nameField = am.Page.Locator("label:has-text('Full Name')").First().Locator(".. input")
		if err := nameField.Clear(); err != nil {
			return fmt.Errorf("could not clear name field: %w", err)
		}
	}
	if err := nameField.Fill(newName); err != nil {
		return fmt.Errorf("could not update name field: %w", err)
	}

	// Clear and update the company field
	companyField := am.Page.Locator("input[id='input-6']")
	if err := companyField.Clear(); err != nil {
		// Try alternative selector if the id-based one fails
		companyField = am.Page.Locator("label:has-text('Company')").First().Locator(".. input")
		if err := companyField.Clear(); err != nil {
			return fmt.Errorf("could not clear company field: %w", err)
		}
	}
	if err := companyField.Fill(newCompany); err != nil {
		return fmt.Errorf("could not update company field: %w", err)
	}

	// Submit the form
	updateButton := am.Page.Locator("button[type='submit']:has-text('Update Profile')")
	if err := updateButton.Click(); err != nil {
		// Try alternative selector if the specific one fails
		if err := am.Page.Locator("button:has-text('Update Profile')").Click(); err != nil {
			return fmt.Errorf("could not click update button: %w", err)
		}
	}

	// Wait for the success message
	if err := am.Page.Locator(".v-snackbar:has-text('Profile updated successfully')").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("profile update success message not found: %w", err)
	}

	// Verify the fields were updated
	updatedName, err := nameField.InputValue()
	if err != nil {
		return fmt.Errorf("could not get updated name: %w", err)
	}
	if updatedName != newName {
		return fmt.Errorf("name was not updated correctly: expected %s, got %s", newName, updatedName)
	}

	updatedCompany, err := companyField.InputValue()
	if err != nil {
		return fmt.Errorf("could not get updated company: %w", err)
	}
	if updatedCompany != newCompany {
		return fmt.Errorf("company was not updated correctly: expected %s, got %s", newCompany, updatedCompany)
	}

	return nil
}

// TestProfileEdit tests editing a user's profile
func TestProfileEdit(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Generate new profile details
	timestamp := time.Now().UnixNano()
	newName := fmt.Sprintf("Updated User %d", timestamp)
	newCompany := fmt.Sprintf("Updated Company %d", timestamp)

	// Edit the user's profile
	err = editUserProfile(am, user, newName, newCompany)
	if err != nil {
		am.T.Fatalf("Failed to update profile: %v", err)
	}

	am.T.Logf("Successfully updated profile for user: %s", user.Email)
}
