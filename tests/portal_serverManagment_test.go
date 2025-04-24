package tests

import (
	"fmt"

	"github.com/playwright-community/playwright-go"
)

// doServerActivate activates a server using Playwright
func doServerAcvite(am *ArtifactManager, owner *User, server *CatalogServer) error {
	if err := loginUser(am, owner.Email, owner.Password); err != nil {
		return fmt.Errorf("login failed before activating server: %w", err)
	}
	// Navigate to the server details page using SLUG
	serverDetailURL := fmt.Sprintf("/servers/%s", server.Slug)
	am.OpenPageWithURL(serverDetailURL)
	am.SaveScreenshot("server_details_for_activate")

	// --- Click Edit Button ---
	editButtonSelector := "button:has-text('Edit')"
	if err := am.ClickWithDebug(editButtonSelector, "edit_button"); err != nil {
		return fmt.Errorf("failed to click edit button: %w", err)
	}

	// --- Wait for Edit Page ---
	expectedEditURLPattern := fmt.Sprintf("**/servers/edit/%s", server.Slug)
	if err := am.Page.WaitForURL(expectedEditURLPattern, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(15000),
		WaitUntil: playwright.WaitUntilStateLoad,
	}); err != nil {
		am.SaveScreenshot("edit_page_navigation_error")
		am.SaveHTML("edit_page_navigation_error")
		return fmt.Errorf("edit page did not load (URL: %s): %w", am.Page.URL(), err)
	}
	am.SaveScreenshot("server_edit_page_for_activate")

	// --- Change Status to Active ---
	// Click on the Status dropdown to open it
	statusDropdownSelector := ".v-select:has(label:has-text('Status'))"
	statusDropdown, err := am.WaitForLocatorWithDebug(statusDropdownSelector, "status_dropdown_wait")
	if err != nil {
		return fmt.Errorf("status dropdown not found: %w", err)
	}
	if err := statusDropdown.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)}); err != nil {
		am.SaveLocatorDebugInfo(statusDropdownSelector, "status_dropdown_click_failed")
		return fmt.Errorf("failed to click status dropdown: %w", err)
	}

	// Wait for dropdown menu items to appear and select "Active"
	activeOptionSelector := ".v-list-item-title:has-text('Active')" // Target the title text
	// Need to find the specific list item, possibly within the context of the opened menu
	// Vuetify menus are often rendered in a separate overlay.
	// Let's try finding the overlay first.
	menuOverlaySelector := "div.v-overlay__content > .v-list" // Common Vuetify menu structure
	menuOverlay, err := am.WaitForLocatorWithDebug(menuOverlaySelector, "status_menu_overlay")
	if err != nil {
		am.SaveScreenshot("status_menu_not_visible")
		return fmt.Errorf("status dropdown menu overlay not found: %w", err)
	}
	activeOption := menuOverlay.Locator(activeOptionSelector)
	if err := activeOption.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)}); err != nil {
		am.SaveLocatorDebugInfo(activeOptionSelector, "active_option_click_failed")
		return fmt.Errorf("failed to click 'Active' status option: %w", err)
	}
	am.SaveScreenshot("status_set_to_active")

	// --- Click Update Button ---
	updateButtonSelector := "button[type='submit']:has-text('Update Core Details')"
	if err := am.ClickWithDebug(updateButtonSelector, "update_server_button"); err != nil {
		return fmt.Errorf("failed to click Update Core Details button: %w", err)
	}

	// Wait for success message (optional but good)
	if _, err := am.WaitForLocatorWithDebug(".v-snackbar:has-text('Server core details updated successfully')", "success_message_wait"); err != nil {
		return fmt.Errorf("not found - Server core details updated successfully: %w", err)
	}

	cancelButtonSelector := "button[type='button']:has-text('Cancel')"
	if err := am.ClickWithDebug(cancelButtonSelector, "cancel_button_click"); err != nil {
		return fmt.Errorf("could not click Update Server button: %w", err)
	}

	// --- Wait for Navigation and Success ---
	// Wait for navigation back to the server details page using the SLUG
	if err := am.Page.WaitForURL("**"+serverDetailURL, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(5000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		am.SaveScreenshot("navigation_error_after_activate")
		am.SaveHTML("navigation_error_after_activate")
		return fmt.Errorf("navigation back to server details page failed after activation (URL: %s): %w", am.Page.URL(), err)
	}

	// Wait for success message
	if _, err := am.WaitForLocatorWithDebug(".v-snackbar:has-text('Server updated successfully')", "activate_success_message"); err != nil {
		am.T.Logf("Warning: Server activation success message not found - proceeding anyway")
	}

	// Optionally, verify the status chip on the details page (requires locating it)
	// Example: statusChip, err := am.WaitForLocatorWithDebug(".v-chip:has-text('Active')", "active_status_chip")
	// require.NoError(am.T, err, "Active status chip not found after activation")

	am.SaveScreenshot("after_server_activate_success")
	am.T.Logf("Server %s activated successfully", server.Slug)
	return nil
}
