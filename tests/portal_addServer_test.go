package tests

import (
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
)

// CatalogServer represents a server in the catalog
type CatalogServer struct {
	ID          string
	Name        string
	Description string
	ServerURL   string
}

// addServer adds a new server to the catalog using Playwright browser automation
func addServer(am *ArtifactManager, user *User) (*CatalogServer, error) {
	// First, log in the user
	err := loginUser(am, user.Email, user.Password)
	if err != nil {
		return nil, err
	}

	// Navigate to servers page
	am.OpenPageWithURL("/servers")

	// Take a screenshot of the servers page
	am.SaveScreenshot("servers_page")

	// Click on Add Server button
	if err := am.ClickWithDebug("button:has-text('Add Server')", "add_server_button"); err != nil {
		return nil, err
	}

	// Wait for the dialog to appear
	addDialog, err := am.WaitForLocatorWithDebug(".v-dialog:visible", "add_server_dialog")
	if err != nil {
		return nil, err
	}

	// Take a screenshot of the add server dialog
	am.SaveScreenshot("add_server_dialog")

	// Fill the server URL
	urlField := addDialog.Locator("input[placeholder*='https://example.com/mcp']")
	if err := urlField.Fill(EXAMPLE_SERVER_URL); err != nil {
		return nil, err
	}

	// Click on Fetch Server Info button
	fetchButton := addDialog.Locator("button:has-text('Fetch Server Info')")
	if err := fetchButton.Click(); err != nil {
		return nil, err
	}

	// Wait for server info to be fetched and processed
	am.T.Logf("Waiting for server info to be fetched")

	am.SaveScreenshot("after_fetch_server_info")

	// Create a new catalog server record with the added server info
	serverName := "Example Server"
	server := &CatalogServer{
		Name:        serverName,
		Description: "Example server for testing",
		ServerURL:   EXAMPLE_SERVER_URL,
	}

	// Wait for navigation to the server details page with longer timeout
	err = am.Page.WaitForURL("**/servers/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000), // 30 second timeout
	})
	if err != nil {
		am.SaveScreenshot("navigation_error")
		return nil, err
	}

	// Take a screenshot after navigation
	am.SaveScreenshot("server_details_landing")

	// Extract the server ID from the URL
	url := am.Page.URL()
	server.ID = extractServerIDFromURL(url)

	am.T.Logf("Server added with ID: %s", server.ID)
	return server, nil
}

// extractServerIDFromURL extracts the server ID from a URL like /servers/123
func extractServerIDFromURL(url string) string {
	// Find the last segment of the URL
	var id string
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '/' {
			id = url[i+1:]
			break
		}
	}
	return id
}

// TestAddServer tests adding a new server to the catalog
func TestAddServer(t *testing.T) {
	// Create artifact manager
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add a new server
	server, err := addServer(am, user)
	if err != nil {
		t.Fatalf("Failed to add server: %v", err)
	}

	t.Logf("Successfully added server with ID: %s", server.ID)

	// Take a screenshot of the server details page
	am.SaveScreenshot("server_details")

	// Verify that tools section exists with a heading
	toolsHeading, err := am.WaitForLocatorWithDebug("h2:has-text('Available Tools')", "tools_heading")
	if err != nil {
		t.Fatalf("Failed to find tools heading: %v", err)
	}

	// Take screenshot of the tools section
	am.SaveScreenshot("tools_section")

	// Dump the HTML of the tools section for debugging
	toolsSection := toolsHeading.Locator("xpath=..")
	toolsSectionHTML, err := toolsSection.InnerHTML()
	if err == nil {
		t.Logf("Tools section HTML: %s", toolsSectionHTML)
	}

	// Look for any elements that might be tool panels
	toolPanels, err := am.Page.Locator("div.v-expansion-panel").All()
	if err != nil {
		t.Fatalf("Failed to locate expansion panels: %v", err)
	}

	t.Logf("Found %d expansion panels", len(toolPanels))

	// Try to find and click a panel that contains the add tool
	found := false
	for i, panel := range toolPanels {
		panelText, err := panel.TextContent()
		if err != nil {
			continue
		}

		t.Logf("Panel %d text: %s", i, panelText)

		// If panel contains "add", click it
		if strings.Contains(strings.ToLower(panelText), "add") {
			err = panel.Click()
			if err != nil {
				t.Fatalf("Failed to click on panel %d: %v", i, err)
			}

			am.SaveScreenshot("add_tool_expanded")
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("Could not find any panel containing the add tool")
	}

	// Check if parameters are displayed
	paramsTable, err := am.WaitForLocatorWithDebug("table", "parameters_table")
	if err != nil {
		t.Fatalf("Failed to find parameters table: %v", err)
	}

	// Check if the parameters table has rows
	rowCount, err := paramsTable.Locator("tbody tr").Count()
	if err != nil {
		t.Fatalf("Failed to count parameter rows: %v", err)
	}

	if rowCount == 0 {
		t.Fatalf("The Add tool has no parameters displayed")
	}

	// Verify parameter details
	t.Logf("Verifying parameter details for the Add tool")

	// Expected parameters
	expectedParams := []struct {
		name        string
		paramType   string
		description string
	}{
		{"a", "number", "First number to add"},
		{"b", "number", "Second number to add"},
	}

	// Check each expected parameter
	for i, expected := range expectedParams {
		// Get the row by index
		paramRow := paramsTable.Locator("tbody tr").Nth(i)

		// Get cells for this row
		cells, err := paramRow.Locator("td").All()
		if err != nil {
			t.Fatalf("Failed to get cells for parameter %s: %v", expected.name, err)
		}

		if len(cells) < 4 {
			t.Fatalf("Parameter row %d doesn't have enough cells", i)
		}

		// Check name
		nameText, err := cells[0].TextContent()
		if err != nil {
			t.Fatalf("Failed to get parameter name: %v", err)
		}
		if nameText != expected.name {
			t.Fatalf("Parameter %d name mismatch: expected '%s', got '%s'", i, expected.name, nameText)
		}

		// Check type
		typeText, err := cells[1].TextContent()
		if err != nil {
			t.Fatalf("Failed to get parameter type: %v", err)
		}
		if !strings.Contains(typeText, expected.paramType) {
			t.Fatalf("Parameter %d type mismatch: expected '%s', got '%s'", i, expected.paramType, typeText)
		}

		// Check description
		descText, err := cells[3].TextContent()
		if err != nil {
			t.Fatalf("Failed to get parameter description: %v", err)
		}
		if descText != expected.description {
			t.Fatalf("Parameter %d description mismatch: expected '%s', got '%s'", i, expected.description, descText)
		}
	}

	t.Logf("Successfully verified Add tool with %d parameters", rowCount)
}
