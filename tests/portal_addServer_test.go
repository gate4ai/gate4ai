package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/tests/env"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require" // Import testify require
)

// CatalogServer represents a server in the catalog
type CatalogServer struct {
	ID          string // Keep ID field, but it might be empty if only slug is extracted from URL
	Slug        string // Add slug field
	Name        string
	Description string
	ServerURL   string
}

// addMCPServer adds a new server to the catalog using Playwright browser automation
func addMCPServer(am *ArtifactManager, user *User, slug string) (*CatalogServer, error) {
	// First, log in the user
	if err := loginUser(am, user.Email, user.Password); err != nil {
		return nil, fmt.Errorf("login failed before adding server: %w", err)
	}

	// Navigate to servers page
	am.OpenPageWithURL("/servers")
	am.SaveScreenshot("servers_page_before_add")

	// Click on Add Server button
	if err := am.ClickWithDebug("#add-server-button", "add_server_button"); err != nil { // Keep original ID for now
		return nil, fmt.Errorf("could not click Add Server button: %w", err)
	}

	// Wait for the dialog to appear and essential elements to be ready
	dialogSelector := ".v-dialog:visible"
	_, err := am.WaitForLocatorWithDebug(dialogSelector, "add_server_dialog_visible")
	if err != nil {
		return nil, fmt.Errorf("add server dialog did not appear: %w", err)
	}

	// Wait specifically for the URL input field within the dialog
	urlFieldSelector := "input[placeholder='Enter the base URL of the server']"
	urlInput, err := am.WaitForLocatorWithDebug(urlFieldSelector, "add_server_dialog_step1_url_input_fail")
	if err != nil {
		am.SaveScreenshot("add_server_dialog_step1_url_input_fail")
		am.SaveHTML("add_server_dialog_step1_url_input_fail")
		return nil, fmt.Errorf("server URL input field did not appear in dialog: %w", err)
	}

	am.SaveScreenshot("add_server_dialog_step1")

	// --- Step 1: Enter URL and Discover ---
	// Fill the server URL using the located input field
	if err := urlInput.Fill(EXAMPLE_MCP2024_SERVER_URL); err != nil {
		am.SaveLocatorDebugInfo(urlFieldSelector, "fill_server_url_failed")
		return nil, fmt.Errorf("failed to fill server URL: %w", err)
	}

	slugFieldSelector := "input[placeholder='my-unique-server-slug']"
	slugField, err := am.WaitForLocatorWithDebug(slugFieldSelector, "wait_for_slug_field")
	if err != nil {
		return nil, fmt.Errorf("slug field did not appear: %w", err)
	}

	if slug != "" {
		time.Sleep(100 * time.Millisecond)
		slugField.Fill(slug)
	} else {
		val, inputErr := slugField.InputValue()
		if inputErr != nil {
			am.T.Logf("Error getting slug input value: %v", inputErr)
		}
		slug = val
	}

	// Wait for any loading indicators to finish
	loadingLocator := am.Page.Locator("[data-testid='add-server-slug-input'] .v-progress-linear--active")

	_ = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(1000),
	})

	err = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateDetached,
		Timeout: playwright.Float(5000),
	})
	if err != nil {
		return nil, fmt.Errorf("Warning: Loading indicator did not disappear: %w", err)
	}

	// Check for error messages in the slug input field
	// Only actual errors will have the v-field--error class on the parent field
	fieldErrorSelector := "[data-testid='add-server-slug-input'] .v-field--error"
	fieldErrorLocator := am.Page.Locator(fieldErrorSelector)

	fieldErrorCount, err := fieldErrorLocator.Count()
	if err == nil && fieldErrorCount > 0 {
		// There is an error state on the field, now get the error message
		errorSelector := "[data-testid='add-server-slug-input'] .v-messages__message"
		errorLocator := am.Page.Locator(errorSelector)

		isVisible, err := errorLocator.First().IsVisible()
		if err == nil && isVisible {
			// Get the error message text
			errorText, err := errorLocator.First().TextContent()
			if err == nil && errorText != "" {
				am.SaveScreenshot("add_server_slug_error")
				am.T.Logf("Slug error detected: %s", errorText)
				return nil, fmt.Errorf("invalid server slug: %s", errorText)
			}
		}
	}

	am.SaveScreenshot("add_server_dialog_step1_filled")

	// Click on Discover Server Type & Info button (use data-testid)
	discoverButtonSelector := "[data-testid='discover-server-button']"
	if err := am.ClickWithDebug(discoverButtonSelector, "discover_server_button"); err != nil {
		return nil, fmt.Errorf("failed to click Discover Server button: %w", err)
	}

	// --- Step 2: Wait for Confirmation and Save ---
	// Wait for Step 2 elements to appear, specifically the "Add MCP Server" button (use data-testid)
	addMcpButtonSelector := "[data-testid='add-mcp-server-button']"
	if _, err := am.WaitForLocatorWithDebug(addMcpButtonSelector, "wait_for_add_mcp_button"); err != nil {
		am.SaveScreenshot("add_server_dialog_step2_fail")
		am.SaveHTML("add_server_dialog_step2_fail")
		return nil, fmt.Errorf("step 2 of add server dialog did not load (Add MCP button not found): %w", err)
	}
	am.SaveScreenshot("add_server_dialog_step2_visible")

	// Optionally fill/verify name field in step 2 (use data-testid)
	nameFieldStep2Selector := "[data-testid='step2-server-name-input'] input" // Target input within data-testid
	nameInputStep2, err := am.WaitForLocatorWithDebug(nameFieldStep2Selector, "wait_for_name_field_step2")
	if err != nil {
		am.T.Logf("Warning: Could not find name input in Step 2: %v", err)
	} else {
		// Example: Fill name if needed
		if err := nameInputStep2.Fill("E2E Example Server " + am.Timestamp); err != nil {
			am.SaveLocatorDebugInfo(nameFieldStep2Selector, "fill_server_name_step2_failed")
			return nil, fmt.Errorf("failed to fill server name in step 2: %w", err)
		}
	}

	// Click the "Add MCP Server" button
	if err := am.ClickWithDebug(addMcpButtonSelector, "add_mcp_server_button_click"); err != nil {
		return nil, fmt.Errorf("failed to click Add MCP Server button: %w", err)
	}

	// Wait for navigation to the new server's details page
	// The URL should now contain the SLUG
	expectedServerUrlPattern := fmt.Sprintf("**/servers/%s", slug)
	if err := am.Page.WaitForURL(expectedServerUrlPattern, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(30000), // 30 second timeout
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		am.SaveScreenshot("add_server_navigation_error")
		am.SaveHTML("add_server_navigation_error")
		return nil, fmt.Errorf("failed to navigate to server details page after adding server (Expected pattern: %s, Current URL: %s): %w", expectedServerUrlPattern, am.Page.URL(), err)
	}

	am.T.Logf("Successfully navigated to new server page: %s", am.Page.URL())
	am.SaveScreenshot("server_details_page_after_add")

	// Create a new catalog server record with the added server info
	server := &CatalogServer{
		ID:          "", // ID (UUID) is not available from URL slug
		Slug:        extractServerSlugFromURL(am.Page.URL()),
		Name:        "E2E Example Server " + am.Timestamp, // Use the name we set
		Description: "",                                   // Description wasn't set in this flow
		ServerURL:   EXAMPLE_MCP2024_SERVER_URL,
	}
	// Verify slug extraction worked
	if server.Slug == "" {
		am.T.Logf("Warning: Could not extract server slug from URL: %s", am.Page.URL())
		// Assign generated slug as fallback if extraction fails
		server.Slug = slug
	}

	am.T.Logf("Server added successfully. Slug: %s", server.Slug)
	return server, nil
}

// extractServerSlugFromURL extracts the server slug from a URL like /servers/the-slug-here
func extractServerSlugFromURL(url string) string {
	parts := strings.Split(strings.Trim(url, "/"), "/")
	// Check if the second to last part is "servers" and there's a part after it
	if len(parts) >= 2 && parts[len(parts)-2] == "servers" {
		return parts[len(parts)-1] // Return the last part (the slug)
	}
	return "" // Return empty if pattern doesn't match
}

// TestAddMCPServer tests adding a new server to the catalog
func TestAddMCPServer(t *testing.T) {
	// Create artifact manager
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	// Add a new server
	server, err := addMCPServer(am, user, "test-add-mcp-server")
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)
	require.NotEmpty(t, server.Slug, "Server slug should not be empty after creation")

	t.Logf("Successfully added server. Slug: %s", server.Slug)

	// Take a screenshot of the server details page
	am.SaveScreenshot("server_details_after_add_verify")

	// Look for any elements that might be tool panels
	toolPanels, err := am.Page.Locator("div.v-expansion-panel").All()
	require.NoError(t, err, "Failed to locate expansion panels")

	t.Logf("Found %d expansion panels", len(toolPanels))
	require.Greater(t, len(toolPanels), 0, "Should find at least one tool panel")

	// Try to find and click a panel that contains the "add" tool
	found := false
	for i, panel := range toolPanels {
		// Find the title element within the panel button
		panelTitle := panel.Locator(".v-expansion-panel-title span.text-subtitle-1")
		panelText, err := panelTitle.TextContent()
		if err != nil {
			am.T.Logf("Could not get text for panel %d title: %v", i, err)
			continue
		}
		panelText = strings.TrimSpace(panelText)
		t.Logf("Panel %d title text: '%s'", i, panelText)

		// If panel title matches "add", click it
		if strings.EqualFold(panelText, "add") {
			err = panelTitle.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)}) // Click the title to expand
			require.NoError(t, err, "Failed to click on panel %d title", i)

			am.SaveScreenshot("add_tool_expanded_verify")
			found = true
			break
		}
	}
	require.True(t, found, "Could not find the panel for the 'add' tool by title")

	// Check if parameters are displayed within the *specific expanded panel's content*
	paramsTable, err := am.WaitForLocatorWithDebug("div.v-expansion-panel--active table", "parameters_table_verify")
	require.NoError(t, err, "Failed to find parameters table within active panel")

	// Check if the parameters table has rows
	rowCount, err := paramsTable.Locator("tbody tr").Count()
	require.NoError(t, err, "Failed to count parameter rows")
	require.Greater(t, rowCount, 0, "The Add tool has no parameters displayed")

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
		// Get the row by index within the found table
		paramRow := paramsTable.Locator("tbody tr").Nth(i)

		// Get cells for this row
		cells, err := paramRow.Locator("td").All()
		require.NoError(t, err, "Failed to get cells for parameter %s", expected.name)
		require.GreaterOrEqual(t, len(cells), 4, "Parameter row %d doesn't have enough cells", i)

		// Check name
		nameText, err := cells[0].TextContent()
		require.NoError(t, err, "Failed to get parameter name")
		require.Equal(t, expected.name, nameText, "Parameter %d name mismatch", i)

		// Check type
		typeText, err := cells[1].TextContent()
		require.NoError(t, err, "Failed to get parameter type")
		require.Contains(t, typeText, expected.paramType, "Parameter %d type mismatch", i)

		// Check description
		descText, err := cells[3].TextContent()
		require.NoError(t, err, "Failed to get parameter description")
		require.Equal(t, expected.description, descText, "Parameter %d description mismatch", i)
	}

	t.Logf("Successfully verified Add tool with %d parameters", rowCount)
}

func TestAddServer_NonUniqueSlug(t *testing.T) {
	// Create artifact manager
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	validSlug := "test-add-server-non-unique-slug" // Use proper lowercase with hyphens format

	// First add with proper format should succeed
	server, err := addMCPServer(am, user, validSlug)
	require.NoError(t, err, "Failed to add server with valid slug")
	require.NotNil(t, server)
	require.NotEmpty(t, server.Slug, "Server slug should not be empty after creation")

	// Add again with same slug, should fail with duplicate error
	_, err = addMCPServer(am, user, validSlug)
	require.Error(t, err, "Should have failed with duplicate slug")
	require.Contains(t, err.Error(), "invalid server slug: This slug is already taken.", "Error should mention that slug already exists")
}

func TestAddServer_NonRigthSlug(t *testing.T) {
	// Create artifact manager
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	// Use a slug with uppercase characters which should cause a validation error
	invalidSlug := "TestAddServer_NonRigthSlug"
	_, err = addMCPServer(am, user, invalidSlug)
	require.Error(t, err, "invalid server slug: Unique identifier (letters, numbers, hyphens). Used in URLs.")
	require.Contains(t, err.Error(), "invalid server slug: Slug must contain only lowercase letters, numbers, and hyphens, and cannot start or end with a hyphen.",
		"Error should mention slug format requirements")
}

func TestAddA2AServer(t *testing.T) {
	a2a_server_url := env.GetURL(env.A2AServerComponentName)
	if a2a_server_url == "" {
		t.Skip("A2A server not running, skipping discovery test")
	}

	// Create artifact manager
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a new user
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	// Add a new server
	server, err := addA2AServer(am, user, "test-add-a2a-server", a2a_server_url)
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)
	require.NotEmpty(t, server.Slug, "Server slug should not be empty after creation")

	t.Logf("Successfully added server. Slug: %s", server.Slug)

	// Take a screenshot of the server details page
	am.SaveScreenshot("server_details_after_add_verify")

	// Verify the server details page content

	// 1. Check for the skill name
	skillNameSelector := ".v-expansion-panel-title .text-subtitle-1"
	skillNameLocator, err := am.WaitForLocatorWithDebug(skillNameSelector, "skill_name_element")
	require.NoError(t, err, "Could not find skill name element")

	skillName, err := skillNameLocator.TextContent()
	require.NoError(t, err, "Could not get skill name text")
	require.Equal(t, "Code Generation", skillName, "Skill name should be 'Code Generation'")

	// 2. Check for the skill description
	descSelector := ".v-expansion-panel-title .text-caption.text-grey"
	descLocator, err := am.WaitForLocatorWithDebug(descSelector, "skill_description_element")
	require.NoError(t, err, "Could not find skill description element")

	description, err := descLocator.TextContent()
	require.NoError(t, err, "Could not get skill description text")
	require.Equal(t, "Generates code snippets or complete files based on user requests, streaming the results.",
		description, "Skill description mismatch")

	// 3. Expand the panel to see the examples
	panelTitleSelector := ".v-expansion-panel-title"
	err = am.ClickWithDebug(panelTitleSelector, "expand_skill_panel")
	require.NoError(t, err, "Failed to click panel title to expand")

	// Wait for panel to expand
	expandedPanelSelector := ".v-expansion-panel--active"
	_, err = am.WaitForLocatorWithDebug(expandedPanelSelector, "expanded_panel")
	require.NoError(t, err, "Panel did not expand")

	// Take a screenshot of the expanded panel
	am.SaveScreenshot("skill_panel_expanded")

	// 4. Check for the specific example
	// Look for the example with the specific text about HTML button
	exampleItems := am.Page.Locator(".v-expansion-panel--active .v-list-item-title")
	exampleCount, err := exampleItems.Count()
	require.NoError(t, err, "Could not count example items")
	require.Greater(t, exampleCount, 0, "No examples found in expanded panel")

	// Check if our specific example exists
	targetExample := "Create an HTML file with a basic button that alerts 'Hello!' when clicked."
	exampleFound := false

	for i := 0; i < exampleCount; i++ {
		example := exampleItems.Nth(i)
		exampleText, err := example.TextContent()
		if err != nil {
			t.Logf("Could not get text for example %d: %v", i, err)
			continue
		}

		// Strip any icons or extra whitespace
		exampleText = strings.TrimSpace(exampleText)
		if strings.Contains(exampleText, targetExample) {
			exampleFound = true
			break
		}
	}

	require.True(t, exampleFound, "Example about HTML button not found")
}

// addA2AServer adds a new A2A server to the catalog using Playwright browser automation
func addA2AServer(am *ArtifactManager, user *User, slug string, a2a_server_url string) (*CatalogServer, error) {
	// First, log in the user
	if err := loginUser(am, user.Email, user.Password); err != nil {
		return nil, fmt.Errorf("login failed before adding A2A server: %w", err)
	}

	// Navigate to servers page
	am.OpenPageWithURL("/servers")
	am.SaveScreenshot("servers_page_before_add_a2a")

	// Click on Add Server button with increased timeout
	if err := am.Page.Locator("#add-server-button").Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000), // 10 seconds timeout
	}); err != nil {
		am.SaveScreenshot("add_a2a_server_button_fail")
		return nil, fmt.Errorf("could not click Add Server button: %w", err)
	}
	am.SaveScreenshot("after_add_server_button_click")

	// Wait for the dialog to appear and essential elements to be ready
	dialogSelector := ".v-dialog:visible"
	_, err := am.WaitForLocatorWithDebug(dialogSelector, "add_a2a_server_dialog_visible")
	if err != nil {
		return nil, fmt.Errorf("add server dialog did not appear: %w", err)
	}

	// Wait specifically for the URL input field within the dialog
	urlFieldSelector := "input[placeholder='Enter the base URL of the server']"
	urlInput, err := am.WaitForLocatorWithDebug(urlFieldSelector, "add_a2a_server_dialog_step1_url_input_fail")
	if err != nil {
		am.SaveScreenshot("add_a2a_server_dialog_step1_url_input_fail")
		am.SaveHTML("add_a2a_server_dialog_step1_url_input_fail")
		return nil, fmt.Errorf("server URL input field did not appear in dialog: %w", err)
	}

	am.SaveScreenshot("add_a2a_server_dialog_step1")

	// --- Step 1: Enter URL and Discover ---
	// Fill the server URL using the located input field with increased timeout
	if err := urlInput.Fill(a2a_server_url, playwright.LocatorFillOptions{
		Timeout: playwright.Float(10000), // 10 seconds timeout
	}); err != nil {
		am.SaveLocatorDebugInfo(urlFieldSelector, "fill_a2a_server_url_failed")
		return nil, fmt.Errorf("failed to fill A2A server URL: %w", err)
	}
	am.SaveScreenshot("after_url_fill")

	// Wait for the slug input field to appear
	slugFieldSelector := "input[placeholder='my-unique-server-slug']"
	slugField, err := am.WaitForLocatorWithDebug(slugFieldSelector, "wait_for_a2a_slug_field")
	if err != nil {
		return nil, fmt.Errorf("slug field did not appear: %w", err)
	}

	// Fill the slug field with increased timeout
	if slug != "" {
		time.Sleep(500 * time.Millisecond) // Increase wait time
		if err := slugField.Fill(slug, playwright.LocatorFillOptions{
			Timeout: playwright.Float(10000), // 10 seconds timeout
		}); err != nil {
			am.SaveLocatorDebugInfo(slugFieldSelector, "fill_slug_failed")
			return nil, fmt.Errorf("failed to fill slug field: %w", err)
		}
	} else {
		val, inputErr := slugField.InputValue()
		if inputErr != nil {
			am.T.Logf("Error getting slug input value: %v", inputErr)
		}
		slug = val
	}
	am.SaveScreenshot("after_slug_fill")

	// Wait for any loading indicators to finish
	loadingLocator := am.Page.Locator("[data-testid='add-server-slug-input'] .v-progress-linear--active")

	_ = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(1000),
	})

	err = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateDetached,
		Timeout: playwright.Float(10000), // Increase timeout
	})
	if err != nil {
		am.T.Logf("Warning: Loading indicator did not disappear: %v", err)
		// Continue anyway
	}

	// Check for error messages in the slug input field
	fieldErrorSelector := "[data-testid='add-server-slug-input'] .v-field--error"
	fieldErrorLocator := am.Page.Locator(fieldErrorSelector)

	fieldErrorCount, err := fieldErrorLocator.Count()
	if err == nil && fieldErrorCount > 0 {
		// There is an error state on the field, now get the error message
		errorSelector := "[data-testid='add-server-slug-input'] .v-messages__message"
		errorLocator := am.Page.Locator(errorSelector)

		isVisible, err := errorLocator.First().IsVisible()
		if err == nil && isVisible {
			// Get the error message text
			errorText, err := errorLocator.First().TextContent()
			if err == nil && errorText != "" {
				am.SaveScreenshot("add_a2a_server_slug_error")
				am.T.Logf("Slug error detected: %s", errorText)
				return nil, fmt.Errorf("invalid server slug: %s", errorText)
			}
		}
	}

	am.SaveScreenshot("add_a2a_server_dialog_step1_filled")

	// Click on Discover Server Type & Info button with increased timeout
	discoverButtonSelector := "[data-testid='discover-server-button']"
	if err := am.Page.Locator(discoverButtonSelector).Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000), // 10 seconds timeout
	}); err != nil {
		am.SaveLocatorDebugInfo(discoverButtonSelector, "discover_a2a_server_button_fail")
		return nil, fmt.Errorf("failed to click Discover Server button: %w", err)
	}
	am.SaveScreenshot("after_discover_button_click")

	// --- Step 2: Wait for Confirmation and Save ---
	// For A2A servers, look for the "Add A2A Server" button
	addA2aButtonSelector := "[data-testid='add-a2a-server-button']"
	_, err = am.WaitForLocatorWithDebug(addA2aButtonSelector, "wait_for_add_a2a_button")
	if err != nil {
		// Let's also try alternative MCP button - the UI may not have a specific A2A button
		addMcpButtonSelector := "[data-testid='add-mcp-server-button']"
		_, err = am.WaitForLocatorWithDebug(addMcpButtonSelector, "fallback_to_mcp_button")
		if err != nil {
			am.SaveScreenshot("add_a2a_server_dialog_step2_fail")
			am.SaveHTML("add_a2a_server_dialog_step2_fail")
			return nil, fmt.Errorf("step 2 of add server dialog did not load (no Add Server button found): %w", err)
		}
		// If we reach here, we'll use the MCP button instead
		addA2aButtonSelector = addMcpButtonSelector
	}
	am.SaveScreenshot("add_a2a_server_dialog_step2_visible")

	// Optionally fill/verify name field in step 2
	nameFieldStep2Selector := "[data-testid='step2-server-name-input'] input"
	nameInputStep2, err := am.WaitForLocatorWithDebug(nameFieldStep2Selector, "wait_for_name_field_step2")
	if err != nil {
		am.T.Logf("Warning: Could not find name input in Step 2: %v", err)
	} else {
		// Fill name with increased timeout
		if err := nameInputStep2.Fill("E2E A2A Example Server "+am.Timestamp, playwright.LocatorFillOptions{
			Timeout: playwright.Float(10000), // 10 seconds timeout
		}); err != nil {
			am.SaveLocatorDebugInfo(nameFieldStep2Selector, "fill_a2a_server_name_step2_failed")
			am.T.Logf("Warning: Failed to fill A2A server name in step 2: %v", err)
			// Continue anyway, the name may be pre-filled
		}
	}
	am.SaveScreenshot("after_name_fill")

	// Click the Add Server button with increased timeout
	if err := am.Page.Locator(addA2aButtonSelector).Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000), // 10 seconds timeout
	}); err != nil {
		am.SaveLocatorDebugInfo(addA2aButtonSelector, "add_a2a_server_button_click_fail")
		return nil, fmt.Errorf("failed to click Add Server button: %w", err)
	}
	am.SaveScreenshot("after_add_button_click")

	// Wait for navigation to the new server's details page
	expectedServerUrlPattern := fmt.Sprintf("**/servers/%s", slug)
	if err := am.Page.WaitForURL(expectedServerUrlPattern, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(30000), // 30 second timeout
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		am.SaveScreenshot("add_a2a_server_navigation_error")
		am.SaveHTML("add_a2a_server_navigation_error")
		return nil, fmt.Errorf("failed to navigate to server details page after adding A2A server (Expected pattern: %s, Current URL: %s): %w", expectedServerUrlPattern, am.Page.URL(), err)
	}

	am.T.Logf("Successfully navigated to new A2A server page: %s", am.Page.URL())
	am.SaveScreenshot("a2a_server_details_page_after_add")

	// Create a new catalog server record with the added server info
	server := &CatalogServer{
		ID:          "",
		Slug:        extractServerSlugFromURL(am.Page.URL()),
		Name:        "E2E A2A Example Server " + am.Timestamp,
		Description: "",
		ServerURL:   a2a_server_url,
	}

	// Verify slug extraction worked
	if server.Slug == "" {
		am.T.Logf("Warning: Could not extract server slug from URL: %s", am.Page.URL())
		server.Slug = slug
	}

	am.T.Logf("A2A Server added successfully. Slug: %s", server.Slug)
	return server, nil
}
