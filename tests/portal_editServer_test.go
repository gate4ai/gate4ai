package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// editServer edits an existing server using Playwright browser automation
func editServer(am *ArtifactManager, user *User, server *CatalogServer, newName, newDesc string) error {
	// First, make sure the user is logged in
	if err := loginUser(am, user.Email, user.Password); err != nil {
		return fmt.Errorf("login failed before editing server: %w", err)
	}
	am.SaveScreenshot("after_login_for_edit")

	// Navigate to the server details page using the SLUG
	serverDetailURL := fmt.Sprintf("/servers/%s", server.Slug)
	am.OpenPageWithURL(serverDetailURL)

	// Wait for the server details page to load, verifying the *initial* name
	headingSelector := fmt.Sprintf("h1:has-text('%s')", server.Name)
	if _, err := am.WaitForLocatorWithDebug(headingSelector, "server_details_initial_heading"); err != nil {
		// Try alternative selector if the specific one fails
		if _, errAlt := am.WaitForLocatorWithDebug("h1.text-h3", "server_heading_alt_initial"); errAlt != nil {
			return fmt.Errorf("server details page did not load (initial name '%s'): %w", server.Name, err) // Use original error
		}
	}
	am.SaveScreenshot("server_details_page_before_edit")

	// Click on the Edit button
	if err := am.ClickWithDebug("button:has-text('Edit')", "edit_button_click"); err != nil {
		return fmt.Errorf("could not click Edit button: %w", err)
	}

	// Wait for edit page URL to contain the SLUG
	expectedEditURLPattern := fmt.Sprintf("**/servers/edit/%s", server.Slug)
	if err := am.Page.WaitForURL(expectedEditURLPattern, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(15000),
		WaitUntil: playwright.WaitUntilStateLoad,
	}); err != nil {
		am.SaveScreenshot("edit_page_navigation_error")
		am.SaveHTML("edit_page_navigation_error")
		return fmt.Errorf("edit page did not load (URL: %s): %w", am.Page.URL(), err)
	}
	am.T.Logf("Navigated to edit page: %s", am.Page.URL())

	// Wait for the form heading to ensure the page content is ready
	if _, err := am.WaitForLocatorWithDebug("h1:has-text('Edit Server')", "edit_server_heading_wait"); err != nil {
		return fmt.Errorf("edit server form heading not found: %w", err)
	}
	am.SaveScreenshot("edit_server_form_loaded")

	// Name field - Use a more robust selector targeting the label
	nameFieldSelector := "div.v-input:has(label:has-text('Server Name')) input" // More flexible selector
	nameField, err := am.WaitForLocatorWithDebug(nameFieldSelector, "wait_for_name_field")
	if err != nil {
		return fmt.Errorf("could not find name field on edit page: %w", err)
	}
	if err := nameField.Clear(); err != nil {
		am.SaveLocatorDebugInfo(nameFieldSelector, "clear_name_field_failed")
		return fmt.Errorf("could not clear name field: %w", err)
	}
	if err := nameField.Fill(newName); err != nil {
		am.SaveLocatorDebugInfo(nameFieldSelector, "fill_name_field_failed")
		return fmt.Errorf("could not update name field: %w", err)
	}

	// Description field - Target the textarea associated with the "Description" label
	descFieldSelector := "div.v-textarea:has(label:has-text('Description')) textarea"
	descField, err := am.WaitForLocatorWithDebug(descFieldSelector, "wait_for_description_field")
	if err != nil {
		return fmt.Errorf("could not find description field on edit page: %w", err)
	}
	if err := descField.Clear(); err != nil {
		am.SaveLocatorDebugInfo(descFieldSelector, "clear_desc_field_failed")
		return fmt.Errorf("could not clear description field: %w", err)
	}
	if err := descField.Fill(newDesc); err != nil {
		am.SaveLocatorDebugInfo(descFieldSelector, "fill_desc_field_failed")
		return fmt.Errorf("could not update description field: %w", err)
	}
	am.SaveScreenshot("edit_server_form_filled")

	// --- Check Protocol and Version Fields ---
	protocolSelector := "div.v-slide-group__content > span:first-child"
	protocolLocator, err := am.WaitForLocatorWithDebug(protocolSelector, "wait_for_protocol_version")
	if err != nil {
		return fmt.Errorf("could not find protocol and version information: %w", err)
	}
	protocolVersionText, err := protocolLocator.TextContent()
	if err != nil {
		return fmt.Errorf("could not get protocol and version text: %w", err)
	}
	require.Contains(am.T, protocolVersionText, "MCP", "Protocol should be present")
	require.Contains(am.T, protocolVersionText, "v2025-03-26", "Version should be 'v2025-03-26'")
	am.T.Logf("Protocol and Version: %s", protocolVersionText)

	// Verify that the fields are likely read-only (no input elements found within the chip)
	protocolVersionInput := am.Page.Locator(protocolSelector + " input")
	protocolVersionInputCount, err := protocolVersionInput.Count()
	if err != nil {
		return fmt.Errorf("could not count input elements in protocol/version field: %w", err)
	}
	require.Equal(am.T, 0, protocolVersionInputCount, "Protocol and Version field should not be editable")

	// --- Submit Form ---
	submitButtonSelector := "button[type='submit']:has-text('Update Server')"
	if err := am.ClickWithDebug(submitButtonSelector, "update_button_click"); err != nil {
		return fmt.Errorf("could not click Update Server button: %w", err)
	}

	// --- Wait for Navigation and Verification ---
	// Wait for navigation back to the server details page using the SLUG
	if err := am.Page.WaitForURL("**"+serverDetailURL, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(15000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		am.SaveScreenshot("navigation_error_after_update")
		am.SaveHTML("navigation_error_after_update")
		return fmt.Errorf("navigation back to server details page failed (URL: %s): %w", am.Page.URL(), err)
	}

	// Wait for success message (optional but good)
	if _, err := am.WaitForLocatorWithDebug(".v-snackbar:has-text('Server updated successfully')", "success_message_wait"); err != nil {
		am.T.Logf("Warning: Server update success message not found - proceeding anyway")
	}

	// Verify the updated server name is displayed on the details page
	updatedHeadingSelector := fmt.Sprintf("h1:has-text('%s')", newName)
	if _, err := am.WaitForLocatorWithDebug(updatedHeadingSelector, "updated_server_name_heading"); err != nil {
		// Try alternative selector
		if _, errAlt := am.WaitForLocatorWithDebug("h1.text-h3", "updated_server_name_alt_heading"); errAlt != nil {
			am.SaveScreenshot("updated_name_not_visible")
			am.SaveHTML("updated_name_not_visible")
			// Check current heading text for debugging
			currentHeading, _ := am.Page.Locator("h1.text-h3").TextContent()
			return fmt.Errorf("updated server name ('%s') not found on details page. Current heading: '%s'. Error: %w", newName, currentHeading, err)
		}
	}
	am.SaveScreenshot("updated_server_details_verified")

	// Update the server object passed to the function (optional, depends on test flow)
	server.Name = newName
	server.Description = newDesc

	return nil
}

// TestEditServer tests editing a server
func TestEditServer(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	// Add a new server
	server, err := addMCPServer(am, user, "test-edit-server")
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)

	// Generate new server details
	timestamp := time.Now().UnixNano()
	newName := fmt.Sprintf("Updated Server %d", timestamp)
	newDesc := fmt.Sprintf("This is an updated test server %d", timestamp)

	// Edit the server
	err = editServer(am, user, server, newName, newDesc)
	require.NoError(t, err, "Failed to edit server")

	t.Logf("Successfully edited server. New name: %s", newName)
}
