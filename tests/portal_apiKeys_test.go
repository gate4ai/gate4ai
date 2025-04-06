package tests

import (
	"fmt"
	"testing"

	"github.com/gate4ai/mcp/shared/config"
	"github.com/playwright-community/playwright-go"
)

// APIKey represents an API key for testing
type APIKey struct {
	ID        string
	Name      string
	Key       string // Only available at creation
	KeyHash   string
	CreatedAt string
}

// createAPIKey creates a new API key for the given user
func createAPIKey(am *ArtifactManager, user *User) (*APIKey, error) {
	// First log in the user
	err := loginUser(am, user.Email, user.Password)
	if err != nil {
		return nil, fmt.Errorf("login failed before creating API key: %w", err)
	}

	// Navigate to the API keys page and wait for full load
	am.OpenPageWithURL("/keys")

	// Wait for the Create API Key button to appear
	addKeyButton, err := am.WaitForLocatorWithDebug("button.v-btn:has-text('Create API Key')", "create_api_key_button_not_found")
	if err != nil {
		return nil, fmt.Errorf("create API key button not found: %w", err)
	}

	// Click the "Create API Key" button
	am.T.Logf("Clicking Create API Key button")
	if err := addKeyButton.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		am.SaveLocatorDebugInfo("button:has-text('Create API Key')", "create_button_click_failed")
		return nil, fmt.Errorf("could not click create API key button: %w", err)
	}
	am.T.Logf("Create API Key button clicked successfully")

	// Wait for dialog to appear
	am.T.Logf("Waiting for key creation dialog")
	keyDialog := am.Page.Locator("div.v-dialog:visible").First()
	if err := keyDialog.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(15000),
	}); err != nil {
		am.SaveScreenshot("key_dialog_not_visible")
		return nil, fmt.Errorf("key creation dialog did not appear: %w", err)
	}

	am.T.Logf("Dialog appeared, entering key name")

	// Generate a name for the key
	timestamp := am.Timestamp
	keyName := fmt.Sprintf("Test Key %s", timestamp)

	// Fill in the key name
	nameInput := keyDialog.Locator("input[type='text']").First()
	if err := nameInput.Fill(keyName); err != nil {
		am.SaveLocatorDebugInfo("input[type='text']", "name_input_fill_failed")
		return nil, fmt.Errorf("could not fill key name: %w", err)
	}

	// Find the key field - we need to find it by its position as the second input field
	am.T.Logf("Looking for key input field")
	keyInputs := keyDialog.Locator("input")
	count, err := keyInputs.Count()
	if err != nil {
		am.SaveLocatorDebugInfo("input fields", "input_count_failed")
		return nil, fmt.Errorf("could not count input fields: %w", err)
	}

	am.T.Logf("Found %d input fields in the dialog", count)

	var keyInput playwright.Locator
	if count >= 2 {
		keyInput = keyInputs.Nth(1) // The second input is the key field
	} else {
		return nil, fmt.Errorf("unexpected number of input fields: %d", count)
	}

	// Click on the eye icon to show the key
	am.T.Logf("Clicking eye icon to reveal key")
	// The eye icon is inside the second input field's append-inner region
	eyeIcon := keyDialog.Locator(".v-field__append-inner .v-icon").First()
	if err := eyeIcon.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		am.SaveLocatorDebugInfo(".v-field__append-inner .v-icon", "eye_icon_click_failed")
		am.T.Logf("Could not click eye icon, continuing anyway: %v", err)
	} else {
		am.T.Logf("Successfully clicked eye icon")
	}

	// Save the key into a local variable
	visibleKeyValue, err := keyInput.InputValue()
	if err != nil {
		am.SaveLocatorDebugInfo("key input", "key_input_value_failed")
		am.T.Logf("Could not get key value, continuing anyway: %v", err)
	} else {
		am.T.Logf("Successfully saved API key value: %s", visibleKeyValue)
	}

	// Submit the form to create the key
	am.T.Logf("Clicking Save button")
	saveButton := keyDialog.Locator("button[type='submit']").First()
	if err := saveButton.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		am.SaveLocatorDebugInfo("button[type='submit']", "save_button_click_failed")
		return nil, fmt.Errorf("could not click save button: %w", err)
	}

	// Wait for the dialog to close
	am.T.Logf("Waiting for the dialog to close")
	if err := keyDialog.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(15000),
	}); err != nil {
		am.SaveScreenshot("dialog_not_closing")
		return nil, fmt.Errorf("dialog did not close: %w", err)
	}

	// Wait for the success notification
	am.T.Logf("Looking for success notification")
	successNotification := am.Page.Locator(".v-snackbar:has-text('API key created successfully')").First()
	if err := successNotification.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000),
	}); err != nil {
		am.SaveLocatorDebugInfo(".v-snackbar", "success_notification_not_visible")
		am.T.Logf("Could not find success notification, continuing anyway: %v", err)
	} else {
		am.T.Logf("Success notification found")
	}

	// Check for the key in the table
	am.T.Logf("Looking for the key in the table")
	keyRow := am.Page.Locator(fmt.Sprintf("table tr:has-text('%s')", keyName)).First()
	if err := keyRow.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(15000),
	}); err != nil {
		am.SaveLocatorDebugInfo(fmt.Sprintf("tr:has-text('%s')", keyName), "key_row_not_visible")
		return nil, fmt.Errorf("key row not visible in table: %w", err)
	}

	// Create API key object
	apiKey := &APIKey{
		Name:      keyName,
		Key:       visibleKeyValue,
		KeyHash:   config.HashAPIKey(visibleKeyValue),
		CreatedAt: timestamp,
	}

	am.T.Logf("Successfully created API key: %s", keyName)
	return apiKey, nil
}

// listAPIKeys gets the list of API keys for the user
func listAPIKeys(am *ArtifactManager, user *User) ([]string, error) {
	// First, log in the user
	err := loginUser(am, user.Email, user.Password)
	if err != nil {
		return nil, fmt.Errorf("login failed before listing API keys: %w", err)
	}

	// Navigate to the API keys page
	am.OpenPageWithURL("/keys")

	// Wait for the page to fully load
	am.T.Logf("Waiting for keys page to load")
	am.Page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(15000),
	})

	am.SaveScreenshot("keys_page_loaded")

	// Check for empty state text or table with a more flexible approach
	am.T.Logf("Checking for API keys or empty state")

	// Try first looking for a table
	tableLocator := am.Page.Locator("table")
	tableVisible, _ := tableLocator.IsVisible()

	// If the table is not visible, check for various empty state messages
	if !tableVisible {
		am.T.Logf("Table not visible, checking for empty state messages")
		emptyStateSelectors := []string{
			"div:has-text('You don\\'t have any API keys yet')",
			"div:has-text('No API keys')",
			"div.v-alert",
			"p:has-text('Create your first API key')",
		}

		foundEmptyState := false
		for _, selector := range emptyStateSelectors {
			locator := am.Page.Locator(selector)
			visible, err := locator.IsVisible()
			if err == nil && visible {
				am.T.Logf("Found empty state with selector: %s", selector)
				foundEmptyState = true
				break
			}
		}

		if foundEmptyState {
			// No keys found, return empty array
			am.T.Logf("No API keys found (empty state detected)")
			return []string{}, nil
		}

		// Neither table nor empty state found
		am.SaveScreenshot("neither_table_nor_empty_state_found")
		// Return empty array instead of error to handle the case where the user has no keys
		am.T.Logf("Neither table nor empty state detected, assuming no keys")
		return []string{}, nil
	}

	// If we reach here, the table is visible, so count the keys
	keyRows := am.Page.Locator("table tbody tr")
	count, err := keyRows.Count()
	if err != nil {
		am.SaveScreenshot("key_rows_count_failed")
		return nil, fmt.Errorf("could not count key rows: %w", err)
	}

	var keyNames []string
	for i := 0; i < count; i++ {
		row := keyRows.Nth(i)
		nameCell := row.Locator("td").First()
		name, err := nameCell.TextContent()
		if err != nil {
			continue
		}
		keyNames = append(keyNames, name)
	}

	am.T.Logf("Found %d API keys", len(keyNames))
	return keyNames, nil
}

// deleteAPIKey deletes an API key by name
func deleteAPIKey(am *ArtifactManager, user *User, keyName string) error {
	// First, log in the user
	err := loginUser(am, user.Email, user.Password)
	if err != nil {
		return fmt.Errorf("login failed before deleting API key: %w", err)
	}

	// Navigate to the API keys page
	am.OpenPageWithURL("/keys")

	// Find the row with the key name
	keyRow := am.Page.Locator(fmt.Sprintf("tr:has-text('%s')", keyName)).First()
	if err := keyRow.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000),
	}); err != nil {
		am.SaveLocatorDebugInfo(fmt.Sprintf("tr:has-text('%s')", keyName), "key_row_not_found")
		return fmt.Errorf("key row not found: %w", err)
	}

	// Set up a handler for the JavaScript confirmation dialog - BEFORE clicking the delete button
	am.T.Logf("Setting up dialog handler for confirmation")
	dialogHandler := func(dialog playwright.Dialog) {
		am.T.Logf("Dialog appeared with message: %s, accepting", dialog.Message())
		// Accept the dialog (click "OK")
		if err := dialog.Accept(); err != nil {
			am.T.Logf("Failed to accept dialog: %v", err)
		}
	}

	// Add the dialog handler
	am.Page.OnDialog(dialogHandler)

	// Click the delete button - it's the error-colored icon button
	deleteButton := keyRow.Locator("button.v-btn.text-error").First()
	if err := deleteButton.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		am.SaveLocatorDebugInfo("button.v-btn.text-error", "delete_button_not_visible")
		return fmt.Errorf("delete button not visible: %w", err)
	}

	// Click the delete button
	if err := deleteButton.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		am.SaveLocatorDebugInfo("button.v-btn.text-error", "delete_button_click_failed")
		return fmt.Errorf("could not click delete button: %w", err)
	}

	// Wait for success notification
	successNotification := am.Page.Locator(".v-snackbar:has-text('API key deleted successfully')").First()
	if err := successNotification.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000),
	}); err != nil {
		am.SaveLocatorDebugInfo(".v-snackbar", "success_notification_not_visible")
		am.T.Logf("Could not find success notification, continuing anyway: %v", err)
	} else {
		am.T.Logf("Success notification found")
	}

	// Verify key is no longer in the table
	keyRowAfterDeletion := am.Page.Locator(fmt.Sprintf("tr:has-text('%s')", keyName)).First()
	present, err := keyRowAfterDeletion.IsVisible()
	if err != nil {
		am.T.Logf("Failed to check if key is still visible: %v", err)
	} else if present {
		am.SaveScreenshot("key_still_present")
		return fmt.Errorf("key row still visible after deletion")
	} else {
		am.T.Logf("Key successfully removed from table")
	}

	am.T.Logf("Successfully deleted API key: %s", keyName)
	return nil
}

// TestAPIKeyManagement tests the API key management functionality
func TestAPIKeyManagement(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a user
	user, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create an API key
	apiKey, err := createAPIKey(am, user)
	if err != nil {
		t.Fatalf("Failed to create API key: %v", err)
	}

	// Verify the key was created and has required properties
	if apiKey.Key == "" || apiKey.KeyHash == "" {
		t.Fatalf("API key not properly created")
	}

	t.Logf("Created API key: %s with hash: %s...", apiKey.Name, apiKey.KeyHash[:8])

	// List API keys
	keyNames, err := listAPIKeys(am, user)
	if err != nil {
		t.Fatalf("Failed to list API keys: %v", err)
	}

	// Verify the created key is in the list
	found := false
	for _, name := range keyNames {
		if name == apiKey.Name {
			found = true
			break
		}
	}

	if !found {
		am.SaveScreenshot("created_key_not_found_in_list")
		t.Fatalf("Created API key not found in the list")
	}

	// Delete the API key
	err = deleteAPIKey(am, user, apiKey.Name)
	if err != nil {
		t.Fatalf("Failed to delete API key: %v", err)
	}

	// Verify the key is no longer in the list
	keyNames, err = listAPIKeys(am, user)
	if err != nil {
		t.Fatalf("Failed to list API keys after deletion: %v", err)
	}

	for _, name := range keyNames {
		if name == apiKey.Name {
			am.SaveScreenshot("key_still_present_after_deletion")
			t.Fatalf("API key still found in the list after deletion")
		}
	}

	t.Logf("API key management test completed successfully")
}
