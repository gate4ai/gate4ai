package tests

import (
	"fmt"
)

// subscribeToServer subscribes a user to a server using Playwright
func doServerAcvite(am *ArtifactManager, owner *User, server *CatalogServer) error {
	err := loginUser(am, owner.Email, owner.Password)
	if err != nil {
		return fmt.Errorf("login failed before adding server: %w", err)
	}
	// Navigate to the server details page
	am.OpenPageWithURL("/servers/" + server.ID)

	// Take a screenshot of the server details page
	am.SaveScreenshot("server_details_for_doActive")

	// Click the Edit button
	editSelector := "button.v-btn:has(i.mdi-pencil):has-text('Edit')"
	err = am.ClickWithDebug(editSelector, "edit_button")
	if err != nil {
		return fmt.Errorf("failed to click edit button: %w", err)
	}

	// Wait for the edit page to load
	am.SaveScreenshot("server_edit_page")

	// Click on the Status dropdown to open it
	statusSelector := ".v-select:has(label:has-text('Status'))"
	err = am.ClickWithDebug(statusSelector, "status_dropdown")
	if err != nil {
		return fmt.Errorf("failed to click status dropdown: %w", err)
	}

	// Wait for dropdown menu to appear and select "Active" option
	activeOptionSelector := ".v-list-item:has-text('Active')"
	err = am.ClickWithDebug(activeOptionSelector, "active_option")
	if err != nil {
		return fmt.Errorf("failed to select Active status: %w", err)
	}

	// Click the Update Server button
	updateButtonSelector := "button[type='submit']:has-text('Update Server')"
	err = am.ClickWithDebug(updateButtonSelector, "update_server_button")
	if err != nil {
		return fmt.Errorf("failed to click Update Server button: %w", err)
	}

	// Take a screenshot after successful activation
	am.SaveScreenshot("after_server_doActive")

	return nil
}
