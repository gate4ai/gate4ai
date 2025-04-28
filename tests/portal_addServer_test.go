package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/tests/env"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// CatalogServer represents a server in the catalog
type CatalogServer struct {
	ID          string
	Slug        string
	Name        string
	Description string
	ServerURL   string
}

// addMCPServer adds a new server to the catalog using Playwright browser automation
// Updated to use data-testid attributes
func addMCPServer(am *ArtifactManager, user *User, slug string) (*CatalogServer, error) {
	if err := loginUser(am, user.Email, user.Password); err != nil {
		return nil, fmt.Errorf("login failed before adding server: %w", err)
	}

	am.OpenPageWithURL("/servers")
	am.SaveScreenshot("servers_page_before_add_mcp")

	// Use data-testid for Add Server button
	if err := am.ClickWithDebug("[data-testid='add-server-button']", "add_server_button"); err != nil {
		return nil, fmt.Errorf("could not click Add Server button: %w", err)
	}

	// Wait for dialog using data-testid for an element inside
	dialogSelector := ".v-dialog:visible [data-testid='add-server-url-input']"
	_, err := am.WaitForLocatorWithDebug(dialogSelector, "add_server_dialog_visible_mcp")
	if err != nil {
		am.SaveScreenshot("add_server_dialog_not_visible_mcp")
		return nil, fmt.Errorf("add server dialog did not appear: %w", err)
	}

	urlFieldSelector := "[data-testid='add-server-url-input'] input"
	urlInput, err := am.WaitForLocatorWithDebug(urlFieldSelector, "add_server_dialog_step1_url_input_wait_mcp")
	if err != nil {
		am.SaveScreenshot("add_server_dialog_step1_url_input_fail_mcp")
		return nil, fmt.Errorf("server URL input field did not appear in dialog: %w", err)
	}
	am.SaveScreenshot("add_server_dialog_step1_mcp")

	// --- Step 1: Enter URL and Discover ---
	exampleMCPURL := env.GetDetails(env.ExampleServerComponentName).(env.ExampleServerDetails).MCP2024URL
	if err := urlInput.Fill(exampleMCPURL); err != nil {
		am.SaveLocatorDebugInfo(urlFieldSelector, "fill_mcp_server_url_failed")
		return nil, fmt.Errorf("failed to fill MCP server URL: %w", err)
	}

	slugFieldSelector := "[data-testid='add-server-slug-input'] input"
	slugField, err := am.WaitForLocatorWithDebug(slugFieldSelector, "wait_for_mcp_slug_field")
	if err != nil {
		return nil, fmt.Errorf("slug field did not appear: %w", err)
	}

	require.NoError(am.T, slugField.Clear(), "Failed to clear slug field")
	if slug != "" {
		time.Sleep(100 * time.Millisecond) // Small delay might help consistency
		if err := slugField.Fill(slug); err != nil {
			am.SaveLocatorDebugInfo(slugFieldSelector, "fill_mcp_slug_failed")
			return nil, fmt.Errorf("failed to fill slug field with '%s': %w", slug, err)
		}
	} else {
		// Wait slightly longer for potential auto-generation if slug wasn't provided
		time.Sleep(500 * time.Millisecond)
		val, inputErr := slugField.InputValue()
		if inputErr != nil {
			am.T.Logf("Error getting auto-generated slug input value: %v", inputErr)
		} else {
			slug = val
		}
		if slug == "" {
			return nil, fmt.Errorf("slug is empty after auto-generation and no slug provided")
		}
	}
	am.T.Logf("Using MCP slug: %s", slug)

	// Wait for slug validation loading indicator (if it appears)
	loadingLocator := am.Page.Locator("[data-testid='add-server-slug-input'] .v-progress-linear--active")
	_ = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(1000)}) // Short wait for visibility
	// Wait longer for it to detach
	err = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateDetached, Timeout: playwright.Float(5000)})
	if err != nil {
		am.T.Logf("Warning: Slug checking loading indicator might not have detached correctly: %v", err)
		// Don't fail the test here, maybe it finished quickly or didn't appear
	}

	// Check for slug field error messages *after* waiting for potential loading indicator
	fieldErrorSelector := "[data-testid='add-server-slug-input'] .v-field--error" // Check for error state class
	fieldErrorLocator := am.Page.Locator(fieldErrorSelector)
	fieldErrorCount, _ := fieldErrorLocator.Count()
	if fieldErrorCount > 0 {
		errorMsgSelector := "[data-testid='add-server-slug-input'] .v-messages__message" // Selector for the error message text
		errorMsgLocator := am.Page.Locator(errorMsgSelector).First()
		isVisible, _ := errorMsgLocator.IsVisible()
		if isVisible {
			errorText, _ := errorMsgLocator.TextContent()
			if errorText != "" && errorText != "Checking..." { // Ignore transient "Checking..." message
				am.SaveScreenshot("add_server_slug_error")
				return nil, fmt.Errorf("invalid server slug: %s", errorText)
			}
		}
	}
	am.SaveScreenshot("add_server_dialog_step1_filled")

	discoverButtonSelector := "[data-testid='discover-server-button']"
	if err := am.ClickWithDebug(discoverButtonSelector, "discover_server_button"); err != nil {
		return nil, fmt.Errorf("failed to click Discover Server button: %w", err)
	}

	// Wait specifically for the MCP button in step 2
	addMcpButtonSelector := "[data-testid='add-mcp-server-button']"
	_, err = am.WaitForLocatorWithDebug(addMcpButtonSelector, "wait_for_add_mcp_button_sse", 30000) // Increased timeout for discovery
	if err != nil {
		am.SaveScreenshot("add_server_dialog_step2_fail_sse_mcp")
		am.SaveHTML("add_server_dialog_step2_fail_sse_mcp")
		logViewerSelector := "[data-testid='discovery-log-viewer']"
		logContent, _ := am.Page.Locator(logViewerSelector).TextContent() // Capture logs on failure
		am.T.Logf("MCP Discovery Log Content on Failure:\n%s", logContent) // Log content
		return nil, fmt.Errorf("step 2 'Add MCP Server' button did not appear after discovery: %w", err)
	}
	am.SaveScreenshot("add_server_dialog_step2_visible_sse_mcp")

	// Fill name in Step 2
	serverNameForTest := "E2E MCP Example Server " + am.Timestamp
	nameFieldStep2Selector := "[data-testid='step2-server-name-input'] input"
	nameInputStep2, err := am.WaitForLocatorWithDebug(nameFieldStep2Selector, "wait_for_name_field_step2_mcp")
	if err != nil {
		am.T.Logf("Warning: Could not find name input in Step 2: %v", err)
		// Attempt to get the auto-filled name if manual find failed
		serverNameValue, valErr := am.Page.Locator(nameFieldStep2Selector).InputValue(playwright.LocatorInputValueOptions{Timeout: playwright.Float(1000)})
		if valErr == nil && serverNameValue != "" {
			am.T.Logf("Using auto-filled server name: %s", serverNameValue)
			serverNameForTest = serverNameValue
		} else {
			// Proceed with the generated name, but log the failure to find the input
			am.T.Logf("Proceeding with generated name as input not found: %s", serverNameForTest)
		}
	} else {
		// Clear and fill if the input was found
		require.NoError(am.T, nameInputStep2.Clear(), "Failed to clear name field step 2")
		if err := nameInputStep2.Fill(serverNameForTest); err != nil {
			am.SaveLocatorDebugInfo(nameFieldStep2Selector, "fill_server_name_step2_failed_mcp")
			return nil, fmt.Errorf("failed to fill server name in step 2: %w", err)
		}
	}

	// Click the Add MCP Server button
	if err := am.ClickWithDebug(addMcpButtonSelector, "add_mcp_server_button_click"); err != nil {
		return nil, fmt.Errorf("failed to click Add MCP Server button: %w", err)
	}

	// Wait for navigation to the server details page
	expectedServerUrlPattern := fmt.Sprintf("**/servers/%s", slug)
	if err := am.Page.WaitForURL(expectedServerUrlPattern, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(30000),              // Generous timeout for potential redirects/loads
		WaitUntil: playwright.WaitUntilStateNetworkidle, // Wait for network activity to cease
	}); err != nil {
		am.SaveScreenshot("add_mcp_server_navigation_error")
		am.SaveHTML("add_mcp_server_navigation_error")
		return nil, fmt.Errorf("failed to navigate to server details page (Expected pattern: %s, Current URL: %s): %w", expectedServerUrlPattern, am.Page.URL(), err)
	}

	am.T.Logf("Successfully navigated to new MCP server page: %s", am.Page.URL())
	am.SaveScreenshot("mcp_server_details_page_after_add")

	// Extract slug from URL for verification (optional, but good practice)
	extractedSlug := extractServerSlugFromURL(am.Page.URL())
	if extractedSlug == "" || extractedSlug != slug {
		am.T.Logf("Warning: Extracted slug '%s' does not match expected '%s' from URL: %s", extractedSlug, slug, am.Page.URL())
		// Use the originally intended slug for the return object if extraction fails
		extractedSlug = slug
	}

	server := &CatalogServer{
		ID:        "", // ID is not easily available on this page, set later if needed
		Slug:      extractedSlug,
		Name:      serverNameForTest, // Use the name we intended to set or the auto-filled one
		ServerURL: exampleMCPURL,
	}

	am.T.Logf("MCP Server added successfully. Slug: %s", server.Slug)
	return server, nil
}

// extractServerSlugFromURL remains the same
func extractServerSlugFromURL(url string) string {
	parts := strings.Split(strings.Trim(url, "/"), "/")
	// Look for 'servers' followed by the slug
	for i, part := range parts {
		if part == "servers" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// addA2AServer adds a new A2A server to the catalog using Playwright browser automation
// Updated to use data-testid attributes
func addA2AServer(am *ArtifactManager, user *User, slug string, a2a_server_url string) (*CatalogServer, error) {
	if err := loginUser(am, user.Email, user.Password); err != nil {
		return nil, fmt.Errorf("login failed before adding A2A server: %w", err)
	}

	am.OpenPageWithURL("/servers")
	am.SaveScreenshot("servers_page_before_add_a2a")

	// Use data-testid
	if err := am.ClickWithDebug("[data-testid='add-server-button']", "add_a2a_server_button"); err != nil {
		return nil, fmt.Errorf("could not click Add Server button: %w", err)
	}

	// Wait for dialog using data-testid for an element inside
	dialogSelector := ".v-dialog:visible [data-testid='add-server-url-input']"
	_, err := am.WaitForLocatorWithDebug(dialogSelector, "add_a2a_server_dialog_visible")
	if err != nil {
		am.SaveScreenshot("add_a2a_server_dialog_not_visible")
		return nil, fmt.Errorf("add server dialog did not appear: %w", err)
	}

	urlFieldSelector := "[data-testid='add-server-url-input'] input"
	urlInput, err := am.WaitForLocatorWithDebug(urlFieldSelector, "add_a2a_server_dialog_step1_url_input")
	if err != nil {
		am.SaveScreenshot("add_a2a_server_dialog_step1_url_input_fail")
		return nil, fmt.Errorf("server URL input field did not appear in dialog: %w", err)
	}
	am.SaveScreenshot("add_a2a_server_dialog_step1")

	// --- Step 1: Enter URL and Discover ---
	if err := urlInput.Fill(a2a_server_url); err != nil {
		am.SaveLocatorDebugInfo(urlFieldSelector, "fill_a2a_server_url_failed")
		return nil, fmt.Errorf("failed to fill A2A server URL: %w", err)
	}

	slugFieldSelector := "[data-testid='add-server-slug-input'] input"
	slugField, err := am.WaitForLocatorWithDebug(slugFieldSelector, "wait_for_a2a_slug_field")
	if err != nil {
		return nil, fmt.Errorf("slug field did not appear: %w", err)
	}

	require.NoError(am.T, slugField.Clear(), "Failed to clear slug field")
	if slug != "" {
		time.Sleep(100 * time.Millisecond)
		if err := slugField.Fill(slug); err != nil {
			am.SaveLocatorDebugInfo(slugFieldSelector, "fill_a2a_slug_failed")
			return nil, fmt.Errorf("failed to fill A2A slug field with '%s': %w", slug, err)
		}
	} else {
		time.Sleep(500 * time.Millisecond)
		val, inputErr := slugField.InputValue()
		if inputErr != nil {
			am.T.Logf("Error getting auto-generated A2A slug input value: %v", inputErr)
		} else {
			slug = val
		}
		if slug == "" {
			return nil, fmt.Errorf("A2A slug is empty after auto-generation and no slug provided")
		}
	}
	am.T.Logf("Using A2A slug: %s", slug)

	// Wait for validation
	loadingLocator := am.Page.Locator("[data-testid='add-server-slug-input'] .v-progress-linear--active")
	_ = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(1000)})
	err = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateDetached, Timeout: playwright.Float(5000)})
	if err != nil {
		am.T.Logf("Warning: A2A Slug checking loading indicator might not have detached correctly: %v", err)
	}

	// Check for slug errors
	fieldErrorSelector := "[data-testid='add-server-slug-input'] .v-field--error"
	fieldErrorLocator := am.Page.Locator(fieldErrorSelector)
	fieldErrorCount, _ := fieldErrorLocator.Count()
	if fieldErrorCount > 0 {
		errorMsgSelector := "[data-testid='add-server-slug-input'] .v-messages__message"
		errorMsgLocator := am.Page.Locator(errorMsgSelector).First()
		isVisible, _ := errorMsgLocator.IsVisible()
		if isVisible {
			errorText, _ := errorMsgLocator.TextContent()
			if errorText != "" && errorText != "Checking..." {
				am.SaveScreenshot("add_a2a_server_slug_error")
				return nil, fmt.Errorf("invalid A2A server slug: %s", errorText)
			}
		}
	}
	am.SaveScreenshot("add_a2a_server_dialog_step1_filled")

	discoverButtonSelector := "[data-testid='discover-server-button']"
	if err := am.ClickWithDebug(discoverButtonSelector, "discover_a2a_server_button"); err != nil {
		return nil, fmt.Errorf("failed to click Discover Server button for A2A: %w", err)
	}

	// --- Wait for SSE and Step 2 ---
	logViewerSelector := "[data-testid='discovery-log-viewer']"
	_, err = am.WaitForLocatorWithDebug(logViewerSelector, "a2a_discovery_log_viewer_wait")
	if err != nil {
		am.SaveScreenshot("add_a2a_server_log_viewer_fail")
		return nil, fmt.Errorf("A2A discovery log viewer did not appear: %w", err)
	}
	am.T.Log("A2A Discovery log viewer appeared.")

	addA2aButtonSelector := "[data-testid='add-a2a-server-button']"
	_, err = am.WaitForLocatorWithDebug(addA2aButtonSelector, "wait_for_add_a2a_button_sse", 30000) // Increased timeout
	if err != nil {
		am.SaveScreenshot("add_a2a_server_dialog_step2_fail_sse")
		am.SaveHTML("add_a2a_server_dialog_step2_fail_sse")
		logContent, _ := am.Page.Locator(logViewerSelector).TextContent()
		am.T.Logf("A2A Discovery Log Content on Failure:\n%s", logContent)
		return nil, fmt.Errorf("step 2 'Add A2A Server' button did not appear: %w", err)
	}
	am.SaveScreenshot("add_a2a_server_dialog_step2_visible_sse")

	serverNameForTest := "E2E A2A Example Server " + am.Timestamp
	nameFieldStep2Selector := "[data-testid='step2-server-name-input'] input"
	nameInputStep2, err := am.WaitForLocatorWithDebug(nameFieldStep2Selector, "wait_for_a2a_name_field_step2")
	if err != nil {
		am.T.Logf("Warning: Could not find A2A name input in Step 2: %v", err)
		// Attempt to get the auto-filled name if manual find failed
		serverNameValue, valErr := am.Page.Locator(nameFieldStep2Selector).InputValue(playwright.LocatorInputValueOptions{Timeout: playwright.Float(1000)})
		if valErr == nil && serverNameValue != "" {
			am.T.Logf("Using auto-filled A2A server name: %s", serverNameValue)
			serverNameForTest = serverNameValue
		} else {
			am.T.Logf("Proceeding with generated A2A name as input not found: %s", serverNameForTest)
		}
	} else {
		// Clear and fill if found
		require.NoError(am.T, nameInputStep2.Clear(), "Failed to clear A2A name field step 2")
		if err := nameInputStep2.Fill(serverNameForTest); err != nil {
			am.SaveLocatorDebugInfo(nameFieldStep2Selector, "fill_a2a_server_name_step2_failed")
			return nil, fmt.Errorf("failed to fill A2A server name in step 2: %w", err)
		}
	}

	if err := am.ClickWithDebug(addA2aButtonSelector, "add_a2a_server_button_click"); err != nil {
		return nil, fmt.Errorf("failed to click Add A2A Server button: %w", err)
	}

	expectedServerUrlPattern := fmt.Sprintf("**/servers/%s", slug)
	if err := am.Page.WaitForURL(expectedServerUrlPattern, playwright.PageWaitForURLOptions{
		Timeout:   playwright.Float(30000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		am.SaveScreenshot("add_a2a_server_navigation_error")
		am.SaveHTML("add_a2a_server_navigation_error")
		return nil, fmt.Errorf("failed to navigate to A2A server details page (Expected: %s, Current: %s): %w", expectedServerUrlPattern, am.Page.URL(), err)
	}

	am.T.Logf("Successfully navigated to new A2A server page: %s", am.Page.URL())
	am.SaveScreenshot("a2a_server_details_page_after_add")

	extractedSlug := extractServerSlugFromURL(am.Page.URL())
	if extractedSlug == "" || extractedSlug != slug {
		am.T.Logf("Warning: Extracted A2A slug '%s' does not match expected '%s' from URL: %s", extractedSlug, slug, am.Page.URL())
		extractedSlug = slug
	}

	server := &CatalogServer{
		ID:        "",
		Slug:      extractedSlug,
		Name:      serverNameForTest, // Use the name potentially updated from auto-fill
		ServerURL: a2a_server_url,
	}

	am.T.Logf("A2A Server added successfully. Slug: %s", server.Slug)
	return server, nil
}

// TestAddMCPServer tests adding a new server to the catalog
// Updated to use data-testid
// TestAddMCPServer tests adding a new server to the catalog
// Updated to use data-testid
func TestAddMCPServer(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	slug := "test-add-mcp-" + am.Timestamp
	server, err := addMCPServer(am, user, slug)
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)
	require.NotEmpty(t, server.Slug, "Server slug should not be empty after creation")
	require.Equal(t, slug, server.Slug, "Server slug mismatch")

	t.Logf("Successfully added server. Slug: %s", server.Slug)
	am.SaveScreenshot("server_details_after_add_verify")

	// --- Verify Tools Section ---
	toolsHeadingSelector := "h2:has-text('Available Tools')" // Corrected selector
	_, err = am.WaitForLocatorWithDebug(toolsHeadingSelector, "tools_section_heading_wait")
	require.NoError(t, err, "Tools section heading not found on server details page")

	// Wait for the expansion panel for the 'add' tool using data-testid
	addToolPanelSelector := ".v-expansion-panel:has(.v-expansion-panel-title:has-text('add'))" // More specific selector
	addToolTitleSelector := addToolPanelSelector + " .v-expansion-panel-title"
	addToolPanelTitle, err := am.WaitForLocatorWithDebug(addToolTitleSelector, "add_tool_panel_title_wait")
	require.NoError(t, err, "Could not find the panel title for the 'add' tool")

	// Click to expand the panel
	err = addToolPanelTitle.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)})
	require.NoError(t, err, "Failed to click on 'add' tool panel title")
	am.SaveScreenshot("add_tool_expanded_verify")

	// Wait for the parameters table within the now active panel using data-testid
	paramsTableSelector := addToolPanelSelector + " [data-testid='tool-parameters-table']" // Use the table's testid
	paramsTable, err := am.WaitForLocatorWithDebug(paramsTableSelector, "parameters_table_verify")
	require.NoError(t, err, "Failed to find parameters table within 'add' tool panel")

	// Count rows in the table
	rowCount, err := paramsTable.Locator("tbody tr").Count()
	require.NoError(t, err, "Failed to count parameter rows")
	require.Greater(t, rowCount, 0, "The Add tool has no parameters displayed")

	// --- Start of Correction ---

	// Locate the specific row for parameter 'a'
	// We filter the rows to find the one containing a cell with data-testid='param-name' and the text 'a'
	paramARowSelector := "tbody tr:has([data-testid='param-name']:has-text('a'))"
	paramARow := paramsTable.Locator(paramARowSelector)
	// Wait for this specific row to be visible
	require.NoError(t, paramARow.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000), // Give it a bit of time
	}), "Row for parameter 'a' not found or not visible")

	// Verify type within the row
	typeACell := paramARow.Locator("[data-testid='param-type']")
	typeAText, err := typeACell.TextContent()
	require.NoError(t, err, "Failed to get text content for type of param 'a'")
	// Check if the text content *contains* number. Could use require.Equal(t, "number", ...) if it's guaranteed exact.
	require.Contains(t, typeAText, "number", "Type for param 'a' mismatch")

	// Verify description within the row
	descACell := paramARow.Locator("[data-testid='param-description']")
	descAText, err := descACell.TextContent()
	require.NoError(t, err, "Failed to get text content for description of param 'a'")
	require.Equal(t, "First number to add", descAText, "Description for param 'a' mismatch")

	// Verify required status within the row
	requiredACell := paramARow.Locator("[data-testid='param-required']")
	requiredAIcon := requiredACell.Locator("i.mdi-check") // Look for the success check icon
	isVisible, err := requiredAIcon.IsVisible()
	require.NoError(t, err, "Error checking visibility of required icon for param 'a'")
	require.True(t, isVisible, "Required check icon for param 'a' not found or not visible")

	t.Logf("Successfully verified Add tool with %d parameters", rowCount)
}

func TestAddServer_NonUniqueSlug(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	validSlug := "test-add-server-non-unique-slug-" + am.Timestamp

	server, err := addMCPServer(am, user, validSlug)
	require.NoError(t, err, "Failed to add server with valid slug the first time")
	require.NotNil(t, server)
	require.NotEmpty(t, server.Slug, "Server slug should not be empty after creation")

	am.T.Logf("Attempting to add server with the same slug: %s", validSlug)
	_, err = addMCPServer(am, user, validSlug)
	require.Error(t, err, "Should have failed with duplicate slug")
	// Error message comes from the slug check logic now
	require.Contains(t, err.Error(), "invalid server slug: This slug is already taken.", "Error message should indicate that slug already exists")
	am.T.Log("Verified adding with duplicate slug failed as expected.")
	am.SaveScreenshot("add_server_non_unique_slug_fail")
}

func TestAddServer_InvalidSlugFormat(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	invalidSlug := "TestAddServer_InvalidFormat-" + am.Timestamp
	_, err = addMCPServer(am, user, invalidSlug)
	require.Error(t, err, "Adding server with invalid slug format should fail")
	// Error message comes from validation rule
	// **NOTE:** The expected error message might slightly differ based on the exact rule implementation in `~/utils/validation`.
	// Check the actual error message from the test run if this fails.
	require.Contains(t, err.Error(), "invalid server slug:", "Error should mention invalid server slug") // Check for prefix
	am.T.Log("Verified adding with invalid slug format failed as expected.")
	am.SaveScreenshot("add_server_invalid_slug_format_fail")
}

func TestAddA2AServer(t *testing.T) {
	a2a_server_url := env.GetDetails(env.ExampleServerComponentName).(env.ExampleServerDetails).A2AURL

	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")

	slug := "test-add-a2a-" + am.Timestamp
	server, err := addA2AServer(am, user, slug, a2a_server_url)
	require.NoError(t, err, "Failed to add A2A server")
	require.NotNil(t, server)
	require.NotEmpty(t, server.Slug, "Server slug should not be empty after creation")
	require.Equal(t, slug, server.Slug, "A2A Server slug mismatch")

	t.Logf("Successfully added A2A server. Slug: %s", server.Slug)
	am.SaveScreenshot("a2a_server_details_after_add_verify")

	// --- Verify Skills Section ---
	// *** FIX: Use correct selector for H2 tag and text-h4 class ***
	skillsHeadingSelector := "h2.text-h4:has-text('Agent Skills')"
	_, err = am.WaitForLocatorWithDebug(skillsHeadingSelector, "skills_section_heading_wait")
	require.NoError(t, err, "Skills section heading not found on A2A server details page")

	// Wait for the expansion panel for the 'scenario_runner' skill
	// Assuming the panel itself doesn't have a specific testid, but the title inside might
	skillPanelSelector := ".v-expansion-panel:has(.v-expansion-panel-title:has-text('A2A Scenario Runner'))"
	skillPanelTitleSelector := skillPanelSelector + " .v-expansion-panel-title"
	skillPanel, err := am.WaitForLocatorWithDebug(skillPanelTitleSelector, "scenario_runner_skill_panel_wait")
	require.NoError(t, err, "Panel for skill 'scenario_runner' not found")

	// Verify skill name within the panel title
	skillNameElem := skillPanel.Locator(".text-subtitle-1")
	skillName, err := skillNameElem.TextContent()
	require.NoError(t, err)
	require.Equal(t, "A2A Scenario Runner", skillName, "Skill name mismatch")

	// Verify skill description within the panel title
	skillDescElem := skillPanel.Locator(".text-caption.text-grey")
	skillDesc, err := skillDescElem.TextContent()
	require.NoError(t, err)
	require.Contains(t, skillDesc, "Runs different A2A test scenarios", "Skill description mismatch")

	// Click to expand the panel
	err = skillPanel.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)})
	require.NoError(t, err, "Failed to click skill panel title to expand")

	// Wait for the content area - Be more specific if possible, e.g., using a data-testid or checking for examples
	// For now, just wait for the panel text area to exist
	panelContentSelector := skillPanelSelector + " .v-expansion-panel-text"
	_, err = am.WaitForLocatorWithDebug(panelContentSelector, "skill_panel_content_wait")
	require.NoError(t, err, "Skill panel content did not become visible")
	am.SaveScreenshot("a2a_skill_panel_expanded")

	// Verify example items using a more specific selector within the content
	exampleSelector := panelContentSelector + " .v-list-item-title" // Assuming examples are in list items
	exampleItems := am.Page.Locator(exampleSelector)
	exampleCount, err := exampleItems.Count()
	require.NoError(t, err, "Could not count example items")
	require.Greater(t, exampleCount, 0, "No examples found in expanded panel")

	// Verify a specific example text
	expectedExampleText := "respond with text \"Hello!\"" // Example from seed data
	exampleFound := false
	for i := 0; i < exampleCount; i++ {
		item := exampleItems.Nth(i)
		itemText, _ := item.TextContent()
		// Normalize whitespace if necessary
		if strings.Contains(strings.Join(strings.Fields(itemText), " "), expectedExampleText) {
			exampleFound = true
			break
		}
	}
	require.True(t, exampleFound, "Expected example '%s' not found", expectedExampleText)
	t.Log("Successfully verified A2A server details and skill information.")
}

func TestAddServer_DiscoveryStreamLog_InvalidURL(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")
	require.NoError(t, loginUser(am, user.Email, user.Password), "Login failed")

	am.OpenPageWithURL("/servers")
	require.NoError(t, am.ClickWithDebug("[data-testid='add-server-button']", "add_server_button_invalid_url"))
	_, err = am.WaitForLocatorWithDebug(".v-dialog:visible [data-testid='add-server-url-input']", "dialog_url_input_invalid_url")
	require.NoError(t, err, "Add server dialog or URL input not ready")

	// --- Use a non-resolvable URL ---
	invalidURL := fmt.Sprintf("http://non-existent-host-%d.invalid", time.Now().UnixNano())
	slug := "test-invalid-url-" + am.Timestamp

	require.NoError(t, am.FillWithDebug("[data-testid='add-server-url-input'] input", invalidURL, "fill_invalid_url"))
	slugField := am.Page.Locator("[data-testid='add-server-slug-input'] input")
	require.NoError(t, slugField.Clear())
	require.NoError(t, slugField.Fill(slug))

	// Wait for slug check (minimal wait)
	loadingLocator := am.Page.Locator("[data-testid='add-server-slug-input'] .v-progress-linear--active")
	_ = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(1000)})
	err = loadingLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateDetached, Timeout: playwright.Float(5000)})
	if err != nil {
		am.T.Logf("Warning: Slug checking loading indicator might not have detached correctly for invalid URL: %v", err)
	}

	// Log any slug validation messages but don't fail unless it's a format error
	fieldErrorSelector := "[data-testid='add-server-slug-input'] .v-field--error" // Check for error state class
	fieldErrorLocator := am.Page.Locator(fieldErrorSelector)
	fieldErrorCount, _ := fieldErrorLocator.Count()
	if fieldErrorCount > 0 {
		errorMsgSelector := "[data-testid='add-server-slug-input'] .v-messages__message" // Selector for the error message text
		errorMsgLocator := am.Page.Locator(errorMsgSelector).First()
		isVisible, _ := errorMsgLocator.IsVisible()
		if isVisible {
			errorText, _ := errorMsgLocator.TextContent()
			am.T.Logf("Slug field validation message for invalid URL: %s", errorText)
			require.NotContains(t, errorText, "Invalid slug format", "Slug format should still be valid")
		}
	}

	am.SaveScreenshot("add_server_dialog_step1_invalid_url_filled")

	// Click discover
	discoverButtonSelector := "[data-testid='discover-server-button']"
	discoverButton := am.Page.Locator(discoverButtonSelector)
	isDisabled, err := discoverButton.IsDisabled()
	require.NoError(t, err, "Failed to check discover button state")
	require.False(t, isDisabled, "Discover button should be enabled")

	require.NoError(t, discoverButton.Click(), "Failed to click discover button for invalid URL")

	// --- Wait for Discovery Completion ---
	am.T.Log("Waiting for discovery attempts to complete...")
	err = discoverButton.Locator(".v-progress-linear").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden, // Wait for loading to finish
		Timeout: playwright.Float(40000),               // Increase timeout
	})
	require.NoError(t, err, "Discovery loading indicator did not disappear after timeout")
	am.T.Log("Discovery attempts finished.")
	am.SaveScreenshot("add_server_dialog_step1_after_invalid_discovery")

	// --- Verify Log Entries are Present ---
	logViewerSelector := "[data-testid='discovery-log-viewer']"

	// Helper function to simply check if a log entry with the prefix exists and is visible
	checkLogEntryExists := func(testIDPrefix, description string) {
		entrySelector := fmt.Sprintf("%s [data-testid^='%s']", logViewerSelector, testIDPrefix)
		locator := am.Page.Locator(entrySelector).First()

		require.NoError(t, locator.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateVisible,
			Timeout: playwright.Float(2000), // Wait for the element to be present
		}), "%s log entry not found or not visible (Selector: %s)", description, entrySelector)

		am.T.Logf("Verified log entry exists for: %s", description)
	}

	// Verify a log entry exists for each protocol attempt
	// We use the most basic part of the test ID (protocol + method + step)
	checkLogEntryExists("log-entry-MCP-SSE/POST-Handshake", "MCP Handshake")
	checkLogEntryExists("log-entry-A2A-GET-GET", "A2A Discovery")
	checkLogEntryExists("log-entry-REST-GET-GET", "REST Discovery (any path)") // Checks if *any* REST attempt log is there

	// --- Verify Dialog State ---
	// Dialog should still be on Step 1
	urlInput, err := am.WaitForLocatorWithDebug("[data-testid='add-server-url-input']", "verify_url_input_still_visible")
	require.NoError(t, err, "URL input is not visible, dialog might have closed or advanced unexpectedly")
	isUrlVisible, _ := urlInput.IsVisible()
	require.True(t, isUrlVisible, "URL input should still be visible on Step 1")

	// Step 2 buttons should NOT be visible
	addMcpButtonSelector := "[data-testid='add-mcp-server-button']"
	addA2aButtonSelector := "[data-testid='add-a2a-server-button']"
	addRestButtonSelector := "[data-testid='add-rest-server-button']"

	mcpBtnVisible, _ := am.Page.Locator(addMcpButtonSelector).IsVisible(playwright.LocatorIsVisibleOptions{Timeout: playwright.Float(100)})
	a2aBtnVisible, _ := am.Page.Locator(addA2aButtonSelector).IsVisible(playwright.LocatorIsVisibleOptions{Timeout: playwright.Float(100)})
	restBtnVisible, _ := am.Page.Locator(addRestButtonSelector).IsVisible(playwright.LocatorIsVisibleOptions{Timeout: playwright.Float(100)})

	require.False(t, mcpBtnVisible, "'Add MCP Server' button should NOT be visible")
	require.False(t, a2aBtnVisible, "'Add A2A Server' button should NOT be visible")
	require.False(t, restBtnVisible, "'Add REST Server' button should NOT be visible")

	// Verify the main fetchError alert might be displayed (optional check)
	fetchErrorAlert := am.Page.Locator(".v-dialog:visible .v-alert[type='error']")
	alertVisible, _ := fetchErrorAlert.IsVisible(playwright.LocatorIsVisibleOptions{Timeout: playwright.Float(500)})
	if alertVisible {
		alertText, _ := fetchErrorAlert.TextContent()
		t.Logf("Found error alert in dialog: %s", alertText)
	} else {
		t.Log("No main fetch error alert found in dialog (expected if errors are only in log).")
	}

	am.SaveScreenshot("discovery_invalid_url_verification_complete")
}