package tests

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/tests/env" // Import env for SmtpServerDetails
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// Test updating basic text and boolean settings
func TestSettings_UpdateBasic(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	adminUser, err := getAdminUser()
	require.NoError(t, err, "Failed to get admin user")
	require.NoError(t, loginUser(am, adminUser.Email, adminUser.Password), "Admin login failed")

	am.OpenPageWithURL("/settings")
	_, err = am.WaitForLocatorWithDebug("h1:has-text('Settings')", "settings_heading_admin")
	require.NoError(t, err, "Failed to load settings page heading")

	// --- Test updating a text field (Portal Base URL) ---
	settingKeyText := "url_how_users_connect_to_the_portal"
	newValueText := "https://new-test-portal-" + am.Timestamp + ".example.com" // Ensure unique value

	am.T.Logf("Updating text setting: %s to %s", settingKeyText, newValueText)
	// Click 'General' tab if not already active
	generalTabSelector := "[data-testid='settings-tab-general']"
	require.NoError(t, am.ClickWithDebug(generalTabSelector, "general_tab_click"), "Failed to click General tab")
	// Wait slightly for tab content potentially loading settings
	time.Sleep(500 * time.Millisecond)

	// Use data-testid for the input field container and the input itself
	inputSelector := fmt.Sprintf("[data-testid='setting-input-%s'] input", settingKeyText)  // The input element

	inputField, err := am.WaitForLocatorWithDebug(inputSelector, fmt.Sprintf("wait_for_%s_input", settingKeyText))
	require.NoError(t, err, "Could not find input field for %s", settingKeyText)

	require.NoError(t, inputField.Fill(newValueText), "Failed to fill input for %s", settingKeyText)
	require.NoError(t, inputField.Blur(), "Failed to blur input for %s", settingKeyText) // Trigger update on blur

	// Wait for loading indicator within the v-input component to disappear
	// Vuetify text fields show loading via a class on the v-input element itself
	inputLoadingSelector := fmt.Sprintf("[data-testid='setting-input-%s'].v-input--loading", settingKeyText)
	loadingIndicator := am.Page.Locator(inputLoadingSelector)

	// Wait for loading indicator to potentially appear briefly
	_ = loadingIndicator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(1000)})
	am.T.Logf("Waiting for loading state to finish for %s...", settingKeyText)
	// Then wait for it to become hidden (or the class to be removed)
	err = loadingIndicator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateHidden, Timeout: playwright.Float(10000)})
	require.NoError(t, err, "Loading state for %s did not finish after blur/update", settingKeyText)
	am.T.Logf("Loading state finished for %s", settingKeyText)

	// Check for persistent error alert *first*
	errorAlertSelector := "[data-testid='settings-error-alert']"
	errorAlert := am.Page.Locator(errorAlertSelector)
	isErrorVisible, _ := errorAlert.IsVisible(playwright.LocatorIsVisibleOptions{Timeout: playwright.Float(100)}) // Quick check if error alert is visible

	if isErrorVisible {
		errorText, _ := errorAlert.TextContent()
		am.SaveScreenshot("settings_text_update_error")
		t.Fatalf("Text setting update failed. Error alert visible: %s", errorText)
	}

	// If no error alert, THEN check for success snackbar
	successSnackbarSelector := ".v-snackbar__content:has-text('updated successfully')" // Check snackbar content
	_, err = am.WaitForLocatorWithDebug(successSnackbarSelector, "update_text_success_snackbar", 5000)
	require.NoError(t, err, "Success snackbar not found for text setting update after loading finished")
	am.T.Logf("Successfully updated text setting: %s", settingKeyText)

	// Verify value persisted after potential re-render/reload
	// Re-fetch the locator in case the element was replaced
	inputField, err = am.WaitForLocatorWithDebug(inputSelector, fmt.Sprintf("refetch_%s_input", settingKeyText))
	require.NoError(t, err, "Could not re-find input field for %s", settingKeyText)
	currentValueText, err := inputField.InputValue()
	require.NoError(t, err, "Failed to get current value for %s", settingKeyText)
	require.Equal(t, newValueText, currentValueText, "Text setting %s did not persist", settingKeyText)
	am.SaveScreenshot("settings_text_update_verified")

	// --- Test updating a boolean switch (Show Server Owner Email) ---
	settingKeyBool := "show_owner_email"
	am.T.Logf("Updating boolean setting: %s", settingKeyBool)

	// Ensure General tab is active
	require.NoError(t, am.ClickWithDebug(generalTabSelector, "general_tab_click_bool"), "Failed to click General tab for bool test")
	time.Sleep(500 * time.Millisecond)

	// Locate the switch container and input using data-testid
	switchContainerSelector := fmt.Sprintf("[data-testid='setting-item-%s']", settingKeyBool)
	switchInputSelector := fmt.Sprintf("[data-testid='setting-input-%s'] input[type='checkbox']", settingKeyBool)
	switchContainer, err := am.WaitForLocatorWithDebug(switchContainerSelector, fmt.Sprintf("wait_for_%s_container", settingKeyBool))
	require.NoError(t, err, "Could not find container for %s", settingKeyBool)

	// Get the actual input element to check its current state
	switchInput := switchContainer.Locator(switchInputSelector)
	initialChecked, err := switchInput.IsChecked()
	require.NoError(t, err, "Failed to check initial state of %s switch", settingKeyBool)
	am.T.Logf("Initial state for %s: %t", settingKeyBool, initialChecked)

	// Click the switch container (usually works better than input for v-switch)
	require.NoError(t, switchInput.Click(), "Failed to click switch for %s", settingKeyBool)

	// Wait for loading indicator within the switch v-input container to disappear
	switchLoadingSelector := fmt.Sprintf("[data-testid='setting-input-%s'].v-input--loading", settingKeyBool)
	loadingIndicator = am.Page.Locator(switchLoadingSelector)
	_ = loadingIndicator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(1000)})
	am.T.Logf("Waiting for loading state to finish for %s...", settingKeyBool)
	err = loadingIndicator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateHidden, Timeout: playwright.Float(10000)})
	require.NoError(t, err, "Loading indicator for %s did not disappear after click", settingKeyBool)
	am.T.Logf("Loading state finished for %s", settingKeyBool)

	// Check for error alert OR success snackbar
	errorAlert = am.Page.Locator(errorAlertSelector)
	isErrorVisible, _ = errorAlert.IsVisible(playwright.LocatorIsVisibleOptions{Timeout: playwright.Float(100)})
	if isErrorVisible {
		errorText, _ := errorAlert.TextContent()
		am.SaveScreenshot("settings_bool_update_error")
		t.Fatalf("Boolean setting update failed. Error alert visible: %s", errorText)
	}

	// If no error, check for success snackbar
	_, err = am.WaitForLocatorWithDebug(successSnackbarSelector, "update_bool_success_snackbar", 5000)
	require.NoError(t, err, "Success snackbar not found for boolean setting update")
	am.T.Logf("Successfully updated boolean setting: %s", settingKeyBool)

	// Verify the switch state changed
	finalChecked, err := switchInput.IsChecked()
	require.NoError(t, err, "Failed to check final state of %s switch", settingKeyBool)
	am.T.Logf("Final state for %s: %t", settingKeyBool, finalChecked)
	require.NotEqual(t, initialChecked, finalChecked, "Boolean setting %s state did not change after click", settingKeyBool)
	am.SaveScreenshot("settings_bool_update_verified")
}

// Test updating SMTP Server Configuration (JSON value) - Applying improved wait logic
func TestSettings_UpdateSMTPServer(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	adminUser, err := getAdminUser()
	require.NoError(t, err, "Failed to get admin user")
	require.NoError(t, loginUser(am, adminUser.Email, adminUser.Password), "Admin login failed")

	am.OpenPageWithURL("/settings")
	_, err = am.WaitForLocatorWithDebug("h1:has-text('Settings')", "settings_heading_admin_smtp")
	require.NoError(t, err, "Failed to load settings page heading")

	// --- Navigate to Email Tab ---
	emailTabSelector := "[data-testid='settings-tab-email']"
	require.NoError(t, am.ClickWithDebug(emailTabSelector, "email_tab_click"), "Failed to click Email tab")
	time.Sleep(500 * time.Millisecond) // Allow tab content

	// --- Open JSON Edit Dialog ---
	settingKeySMTP := "email_smtp_server"
	am.T.Logf("Opening JSON editor for setting: %s", settingKeySMTP)
	editButtonSelector := fmt.Sprintf("[data-testid='setting-edit-json-%s']", settingKeySMTP)
	editButtonContainerSelector := fmt.Sprintf("[data-testid='setting-item-%s']", settingKeySMTP) // Container for loading check

	require.NoError(t, am.ClickWithDebug(editButtonSelector, "edit_smtp_json_button"), "Failed to click Edit JSON for SMTP")

	// Wait for dialog using its data-testid
	dialogSelector := "[data-testid='edit-json-dialog']"
	dialog, err := am.WaitForLocatorWithDebug(dialogSelector, "smtp_json_dialog_wait")
	require.NoError(t, err, "Edit JSON dialog did not appear for SMTP")
	am.SaveScreenshot("settings_smtp_edit_dialog_open")

	// --- Prepare and Fill JSON ---
	newSMTPConfig := env.SmtpServerDetails{
		Host:   "smtp.updated-test-" + am.Timestamp + ".com", // Unique host
		Port:   587,
		Secure: false,
		Auth: struct {
			User string `json:"user"`
			Pass string `json:"pass"`
		}{User: "updated-user-" + am.Timestamp + "@example.com", Pass: "updated-pass-123"},
	}
	newSMTPJsonBytes, err := json.MarshalIndent(newSMTPConfig, "", "  ")
	require.NoError(t, err, "Failed to marshal new SMTP config to JSON")
	newSMTPJsonString := string(newSMTPJsonBytes)

	am.T.Logf("Filling JSON editor with:\n%s", newSMTPJsonString)
	textareaSelector := "[data-testid='edit-json-textarea'] textarea" // Target the textarea inside
	textarea, err := am.WaitForLocatorWithDebug(textareaSelector, "smtp_json_textarea_wait")
	require.NoError(t, err, "Could not find textarea in SMTP JSON dialog")
	require.NoError(t, textarea.Fill(newSMTPJsonString), "Failed to fill SMTP JSON textarea")
	am.SaveScreenshot("settings_smtp_edit_dialog_filled")

	// --- Save JSON ---
	saveButtonSelector := "[data-testid='edit-json-save-button']"
	require.NoError(t, dialog.Locator(saveButtonSelector).Click(), "Failed to click Save in SMTP JSON dialog")

	// --- Wait for Loading State on the original Edit Button's container ---
	loadingIndicatorSelector := editButtonContainerSelector + " .v-progress-circular" // Look for loader within the setting item
	loadingIndicator := am.Page.Locator(loadingIndicatorSelector)
	_ = loadingIndicator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(1000)})
	am.T.Logf("Waiting for loading state to finish for %s after JSON save...", settingKeySMTP)
	err = loadingIndicator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateHidden, Timeout: playwright.Float(10000)})
	require.NoError(t, err, "Loading indicator for %s did not disappear after saving JSON", settingKeySMTP)
	am.T.Logf("Loading state finished for %s after JSON save", settingKeySMTP)

	// --- Verify Dialog Closed and Check for Errors/Success ---
	err = dialog.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateHidden, Timeout: playwright.Float(5000)})
	require.NoError(t, err, "SMTP JSON dialog did not close after save")

	errorAlertSelector := "[data-testid='settings-error-alert']"
	errorAlert := am.Page.Locator(errorAlertSelector)
	isErrorVisible, _ := errorAlert.IsVisible(playwright.LocatorIsVisibleOptions{Timeout: playwright.Float(100)})
	if isErrorVisible {
		errorText, _ := errorAlert.TextContent()
		am.SaveScreenshot("settings_smtp_update_error")
		t.Fatalf("SMTP setting update failed. Error alert visible: %s", errorText)
	}

	// If no error, check for success snackbar
	successSnackbarSelector := ".v-snackbar__content:has-text('updated successfully')"
	_, err = am.WaitForLocatorWithDebug(successSnackbarSelector, "update_smtp_success_snackbar", 5000)
	require.NoError(t, err, "Success snackbar not found for SMTP setting update after loading finished")
	am.T.Logf("Successfully updated SMTP setting: %s", settingKeySMTP)
	am.SaveScreenshot("settings_smtp_update_success")

	// --- Verify Value Persisted (Re-open dialog and check content) ---
	am.T.Logf("Re-opening JSON editor to verify persistence: %s", settingKeySMTP)
	require.NoError(t, am.ClickWithDebug(editButtonSelector, "reopen_edit_smtp_json_button"), "Failed to re-click Edit JSON for SMTP")
	dialog, err = am.WaitForLocatorWithDebug(dialogSelector, "reopen_smtp_json_dialog_wait")
	require.NoError(t, err, "Edit JSON dialog did not reappear for SMTP verification")

	textarea, err = am.WaitForLocatorWithDebug(textareaSelector, "reopen_smtp_json_textarea_wait")
	require.NoError(t, err, "Could not find textarea in reopened SMTP JSON dialog")

	currentTextareaValue, err := textarea.InputValue()
	require.NoError(t, err, "Failed to get textarea value for verification")

	var currentValueMap map[string]interface{}
	err = json.Unmarshal([]byte(currentTextareaValue), &currentValueMap)
	require.NoError(t, err, "Failed to unmarshal current textarea value from JSON")

	var originalValueMap map[string]interface{}
	originalJsonBytes, _ := json.Marshal(newSMTPConfig) // Use regular marshal for map conversion
	err = json.Unmarshal(originalJsonBytes, &originalValueMap)
	require.NoError(t, err, "Failed to unmarshal original input value to map")

	require.Equal(t, originalValueMap, currentValueMap, "SMTP setting value did not persist correctly")
	am.T.Logf("Verified SMTP setting persistence")
	am.SaveScreenshot("settings_smtp_update_verified")

	// Close the verification dialog
	cancelButtonSelector := "[data-testid='edit-json-cancel-button']"
	require.NoError(t, dialog.Locator(cancelButtonSelector).Click(), "Failed to click Cancel in verification dialog")
}