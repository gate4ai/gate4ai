package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/gateway/clients/mcpClient"
	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/tests/env"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configureServerHeaders uses Playwright to set Server-level headers
func configureServerHeaders(am *ArtifactManager, owner *User, serverSlug string, headers map[string]string) error {
	if err := loginUser(am, owner.Email, owner.Password); err != nil {
		return fmt.Errorf("login failed before configuring server headers: %w", err)
	}
	editURL := fmt.Sprintf("/servers/edit/%s", serverSlug)
	am.OpenPageWithURL(editURL)
	am.SaveScreenshot("edit_page_for_server_headers")

	// Click "Manage Server Headers" button
	btnSelector := "button:has-text('Manage Server Headers')"
	if err := am.ClickWithDebug(btnSelector, "manage_server_headers_btn"); err != nil {
		return fmt.Errorf("failed to click manage server headers button: %w", err)
	}

	// Wait for dialog
	dialogSelector := ".v-dialog:visible:has-text('Edit Server Headers')"
	headersDialog, err := am.WaitForLocatorWithDebug(dialogSelector, "server_headers_dialog")
	if err != nil {
		return fmt.Errorf("server headers dialog did not appear: %w", err)
	}
	am.SaveScreenshot("server_headers_dialog_open")

	// Remove existing headers (if any) - click minus buttons
	minusButtons := headersDialog.Locator("button:has(i.mdi-minus-circle)")
	count, _ := minusButtons.Count()
	for i := 0; i < count; i++ {
		if err := minusButtons.First().Click(); err != nil { // Click the first one repeatedly
			am.T.Logf("Warning: failed to click remove header button: %v", err)
		}
	}

	// Add new headers
	for key, value := range headers {
		// Click "Add Header"
		if err := headersDialog.Locator("button:has-text('Add Header')").Click(); err != nil {
			return fmt.Errorf("failed to click add header button: %w", err)
		}
		// Fill key and value in the *last* row added
		keyInput := headersDialog.Locator("[data-testid='key'] input").Last()
		valueInput := headersDialog.Locator("[data-testid='value'] input").Last()
		if err := keyInput.Fill(key); err != nil {
			return fmt.Errorf("failed to fill header key '%s': %w", key, err)
		}
		if err := valueInput.Fill(value); err != nil {
			return fmt.Errorf("failed to fill header value for key '%s': %w", key, err)
		}
	}
	am.SaveScreenshot("server_headers_dialog_filled")

	// Save headers
	saveBtnSelector := "button:has-text('Save Headers')"
	if err := headersDialog.Locator(saveBtnSelector).Click(); err != nil {
		am.SaveLocatorDebugInfo(saveBtnSelector, "save_server_headers_click_fail")
		return fmt.Errorf("failed to click save server headers button: %w", err)
	}

	// Wait for dialog to close
	err = headersDialog.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(5000),
	})
	if err != nil {
		am.SaveScreenshot("server_headers_dialog_not_closed")
		return fmt.Errorf("server headers dialog did not close: %w", err)
	}
	am.T.Log("Server headers configured successfully")
	return nil
}

type SubscriptionTemplates map[string]struct {
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// configureSubscriptionTemplate uses Playwright to set the subscription header template
func configureSubscriptionTemplate(am *ArtifactManager, owner *User, serverSlug string, templates SubscriptionTemplates) error {
	if err := loginUser(am, owner.Email, owner.Password); err != nil {
		return fmt.Errorf("login failed before configuring subscription template: %w", err)
	}
	editURL := fmt.Sprintf("/servers/edit/%s", serverSlug)
	am.OpenPageWithURL(editURL)
	am.SaveScreenshot("edit_page_for_sub_template")

	// Click "Manage Subscription Template" button
	btnSelector := "button:has-text('Manage Subscription Template')"
	if err := am.ClickWithDebug(btnSelector, "manage_sub_template_btn"); err != nil {
		return fmt.Errorf("failed to click manage subscription template button: %w", err)
	}

	// Wait for dialog
	dialogSelector := ".v-dialog:visible:has-text('Edit Subscription Header Template')"
	templateDialog, err := am.WaitForLocatorWithDebug(dialogSelector, "sub_template_dialog")
	if err != nil {
		return fmt.Errorf("subscription template dialog did not appear: %w", err)
	}
	am.SaveScreenshot("sub_template_dialog_open")

	// Remove existing items (if any)
	minusButtons := templateDialog.Locator("button:has(i.mdi-delete)")
	count, _ := minusButtons.Count()
	for i := 0; i < count; i++ {
		if err := minusButtons.First().Click(); err != nil {
			am.T.Logf("Warning: failed to click remove template item button: %v", err)
		}
	}

	// Add new template items
	for key, template := range templates {
		// Click "Add Template Header"
		if err := templateDialog.Locator("button:has-text('Add Template Header')").Click(); err != nil {
			return fmt.Errorf("failed to click add template item button: %w", err)
		}
		// Fill details in the *last* row added
		itemRow := templateDialog.Locator("[data-testid='template-item']").Last()
		if err := itemRow.Locator("[data-testid='key'] input").Fill(key); err != nil {
			return fmt.Errorf("failed to fill template key '%s': %w", key, err)
		}
		if err := itemRow.Locator("[data-testid='description'] input").Fill(template.Description); err != nil {
			return fmt.Errorf("failed to fill template description for '%s': %w", key, err)
		}
		if template.Required {
			if err := itemRow.Locator("[data-testid='required'] input").Check(); err != nil {
				return fmt.Errorf("failed to check required for '%s': %w", key, err)
			}
		}
	}
	am.SaveScreenshot("sub_template_dialog_filled")

	// Save template
	saveBtnSelector := "button:has-text('Save Template')"
	if err := templateDialog.Locator(saveBtnSelector).Click(); err != nil {
		am.SaveLocatorDebugInfo(saveBtnSelector, "save_sub_template_click_fail")
		return fmt.Errorf("failed to click save template button: %w", err)
	}

	// Wait for dialog to close
	err = templateDialog.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(5000),
	})
	if err != nil {
		am.SaveScreenshot("sub_template_dialog_not_closed")
		return fmt.Errorf("subscription template dialog did not close: %w", err)
	}
	am.T.Log("Subscription template configured successfully")
	return nil
}

// subscribeWithHeaders subscribes a user, handling the header values dialog
func subscribeWithHeaders(am *ArtifactManager, user *User, server *CatalogServer, headerValues map[string]string) error {
	if err := loginUser(am, user.Email, user.Password); err != nil {
		return fmt.Errorf("login failed before subscribing: %w", err)
	}
	serverDetailURL := fmt.Sprintf("/servers/%s", server.Slug)
	am.OpenPageWithURL(serverDetailURL)
	am.SaveScreenshot("server_details_for_subscribe_with_headers")

	// Click Subscribe
	subscribeBtnSelector := `[data-testid="server-subscribe-button"]`
	if err := am.ClickWithDebug(subscribeBtnSelector, "subscribe_button"); err != nil {
		return fmt.Errorf("failed to find/click subscribe button: %w", err)
	}

	// Wait for Header Values Dialog
	dialogSelector := ".v-dialog:visible:has-text('Configure Subscription Headers')"
	valuesDialog, err := am.WaitForLocatorWithDebug(dialogSelector, "sub_values_dialog")
	if err != nil {
		return fmt.Errorf("subscription header values dialog did not appear: %w", err)
	}
	am.SaveScreenshot("sub_values_dialog_open")

	// Fill values
	for key, value := range headerValues {
		// Construct a more specific selector for the input based on the label
		inputSelector := fmt.Sprintf("div.v-input:has(label:text-is('%s')) input", key) // Assumes label text matches key
		// Fallback if label includes asterisk for required
		inputSelectorFallback := fmt.Sprintf("div.v-input:has(label:text-is('%s *')) input", key)

		inputField := valuesDialog.Locator(inputSelector)
		isVisible, _ := inputField.IsVisible()
		if !isVisible {
			inputField = valuesDialog.Locator(inputSelectorFallback) // Try fallback
		}

		if err := inputField.Fill(value); err != nil {
			am.SaveLocatorDebugInfo(inputSelector, fmt.Sprintf("fill_sub_header_%s_fail", key))
			return fmt.Errorf("failed to fill subscription header '%s': %w", key, err)
		}
	}
	am.SaveScreenshot("sub_values_dialog_filled")

	// Click Confirm Subscription
	confirmBtnSelector := "button:has-text('Confirm Subscription')"
	if err := valuesDialog.Locator(confirmBtnSelector).Click(); err != nil {
		am.SaveLocatorDebugInfo(confirmBtnSelector, "confirm_sub_click_fail")
		return fmt.Errorf("failed to click confirm subscription button: %w", err)
	}

	// Wait for dialog to close
	err = valuesDialog.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(5000),
	})
	if err != nil {
		// Check for error alert *within* the dialog if it didn't close
		errorAlert := valuesDialog.Locator(".v-alert[type='error']")
		alertVisible, _ := errorAlert.IsVisible()
		if alertVisible {
			alertText, _ := errorAlert.TextContent()
			return fmt.Errorf("error saving subscription headers: %s", alertText)
		}
		am.SaveScreenshot("sub_values_dialog_not_closed")
		return fmt.Errorf("subscription values dialog did not close: %w", err)
	}

	// Wait for confirmation (Unsubscribe button or success message)
	unsubscribeBtnSelector := "button:has-text('Unsubscribe')"
	_, err = am.WaitForLocatorWithDebug(unsubscribeBtnSelector, "unsubscribe_button_visible_after_subscribe_headers")
	if err != nil {
		if _, errSnack := am.WaitForLocatorWithDebug(".v-snackbar:has-text('Subscribed!')", "subscribe_success_snackbar_headers"); errSnack != nil {
			am.SaveScreenshot("subscription_with_headers_confirmation_failed")
			return fmt.Errorf("subscription confirmation failed after providing headers: %w", err)
		}
		am.T.Logf("Warning: Unsubscribe button not found after header sub, but success snackbar appeared.")
	}

	am.T.Logf("User %s successfully subscribed with headers to server %s", user.Email, server.Slug)
	return nil
}

// callGetHeadersTool calls the getHeaders tool via the gateway and returns the parsed headers map
func callGetHeadersTool(am *ArtifactManager, apiKey string) (http.Header, error) {
	gatewayURL := env.GetURL(env.GatewayComponentName)
	if gatewayURL == "" {
		return nil, fmt.Errorf("gateway URL not available")
	}
	mcpURL := gatewayURL + transport.MCP2024_PATH + "?" + transport.MCP2024_AUTH_KEY + "=" + apiKey

	am.T.Logf("Calling getHeaders via Gateway MCP endpoint: %s", mcpURL)

	// Create MCP client (minimal version for just this call)
	client, err := mcpClient.New("test-getheaders-client", mcpURL, am.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create mcp client: %w", err)
	}
	session := client.NewSession(context.Background(), mcpClient.WithAuthenticationBearer(apiKey))
	defer session.Close()

	// Wait for session to initialize
	initErr := <-session.Open()
	if initErr != nil {
		return nil, fmt.Errorf("failed to initialize mcp session: %w", initErr)
	}
	am.T.Log("MCP Session Initialized")

	// Call the getHeaders tool
	resultChan := session.CallTool(context.Background(), "getHeaders", nil) // No arguments needed
	result := <-resultChan

	if result.Error != nil {
		return nil, fmt.Errorf("getHeaders tool call failed: %w", result.Error)
	}
	if result.Result == nil || len(result.Result.Content) == 0 {
		return nil, fmt.Errorf("getHeaders tool call returned no content")
	}

	// Expecting a single text part containing JSON
	content := result.Result.Content[0]
	if content.Type != "text" || content.Text == nil {
		return nil, fmt.Errorf("getHeaders tool call returned unexpected content type: %s", content.Type)
	}

	// Parse the JSON string from the text part
	var receivedHeaders http.Header
	if err := json.Unmarshal([]byte(*content.Text), &receivedHeaders); err != nil {
		am.T.Logf("Received headers text: %s", *content.Text)
		return nil, fmt.Errorf("failed to parse headers JSON from tool result: %w", err)
	}

	am.T.Logf("Successfully received headers from backend: %d headers", len(receivedHeaders))
	return receivedHeaders, nil
}

// TestE2EHeaders tests the full header flow: Server -> Subscription Template -> Subscription Values -> Gateway -> Backend
func TestE2EHeaders(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// 1. Create Users
	owner, err := createUser(am)
	require.NoError(t, err, "Failed to create owner user")
	subscriber, err := createUser(am)
	require.NoError(t, err, "Failed to create subscriber user")

	// 2. Add Server
	serverSlug := "test-header-server"
	server, err := addMCPServer(am, owner, serverSlug)
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)

	// 3. Configure Server Headers
	serverHeaders := map[string]string{
		"X-Server-Type":      "MCP-Test",
		"X-Server-Region":    "e2e-region",
		"Overwrite":          "server-overwrite",        // Test overwrite
		"x-lowercase-server": "mixed-case-server-value", // Test mixed case key
	}
	require.NoError(t, configureServerHeaders(am, owner, serverSlug, serverHeaders), "Failed to configure server headers")

	// 4. Configure Subscription Header Template
	subscriptionTemplate := SubscriptionTemplates{
		"X-Tenant-ID":            {"Your Tenant ID", true},
		"X-Project-Name":         {"Project Name", false},            // Non-required field
		"Overwrite":              {"User  (Overrides Server)", true}, // Test overwrite
		"x-lowercase-subscriber": {"mixed-case-sub-value", true},     // Test mixed case key
	}
	require.NoError(t, configureSubscriptionTemplate(am, owner, serverSlug, subscriptionTemplate), "Failed to configure subscription template")

	// 5. Activate Server (Needed for subscription)
	require.NoError(t, doServerAcvite(am, owner, server), "Failed to activate server")

	// 6. Subscriber Subscribes with Header Values
	headerValues := map[string]string{
		"X-Tenant-ID":            "tenant-12345",
		"X-Project-Name":         "E2E Test Project",
		"Overwrite":              "subscriber overwrite",
		"x-lowercase-subscriber": "sub-value-provided",
		// "X-Extra-Header": "This should be ignored", // Test extra header (should be ignored by PUT)
	}
	require.NoError(t, subscribeWithHeaders(am, subscriber, server, headerValues), "Failed to subscribe with headers")

	// 7. Create API Key for Subscriber
	apiKey, err := createAPIKey(am, subscriber)
	require.NoError(t, err, "Failed to create API key for subscriber")
	require.NotEmpty(t, apiKey.Key)

	// 8. Call getHeaders Tool via Gateway
	receivedHeaders, err := callGetHeadersTool(am, apiKey.Key)
	require.NoError(t, err, "Failed to call getHeaders tool via gateway")
	require.NotNil(t, receivedHeaders)

	// 9. Verify Headers (Case-insensitive check for keys)
	// Normalize received headers to lowercase keys for assertion
	normalizedReceived := make(map[string]string)
	for k, v := range receivedHeaders {
		normalizedReceived[strings.ToLower(k)] = v[0]
	}

	// --- System Headers ---
	require.Contains(t, normalizedReceived, "gate4ai-user-id", "System header Gate4ai-User-Id missing")
	assert.Equal(t, subscriber.ID, normalizedReceived["gate4ai-user-id"], "User ID mismatch")
	require.Contains(t, normalizedReceived, "gate4ai-server-slug", "System header Gate4ai-Server-Slug missing")
	assert.Equal(t, serverSlug, normalizedReceived["gate4ai-server-slug"], "Server slug mismatch")
	require.Contains(t, normalizedReceived, "x-forwarded-for", "System header X-Forwarded-For missing")
	// Cannot easily assert the exact IP, just check presence

	// --- Server Headers ---
	require.Contains(t, normalizedReceived, "x-server-type", "Server header X-Server-Type missing")
	assert.Equal(t, "MCP-Test", normalizedReceived["x-server-type"])
	require.Contains(t, normalizedReceived, "x-server-region", "Server header X-Server-Region missing")
	assert.Equal(t, "e2e-region", normalizedReceived["x-server-region"])
	require.Contains(t, normalizedReceived, "x-lowercase-server", "Lowercase server header missing") // Check lowercase key
	assert.Equal(t, "mixed-case-server-value", normalizedReceived["x-lowercase-server"])

	// --- Subscription Headers ---
	require.Contains(t, normalizedReceived, "x-tenant-id", "Subscription header X-Tenant-ID missing")
	assert.Equal(t, "tenant-12345", normalizedReceived["x-tenant-id"])
	require.Contains(t, normalizedReceived, "x-project-name", "Subscription header X-Project-Name missing")
	assert.Equal(t, "E2E Test Project", normalizedReceived["x-project-name"])
	require.Contains(t, normalizedReceived, "x-lowercase-subscriber", "Lowercase subscriber header missing") // Check lowercase key
	assert.Equal(t, "sub-value-provided", normalizedReceived["x-lowercase-subscriber"])

	require.Contains(t, normalizedReceived, "overwrite", "Overwrite header missing")
	assert.Equal(t, "server-overwrite", normalizedReceived["overwrite"], "Overwrite header priority incorrect")

	// --- Priority Check (System Override - using Gate4ai-User-Id as example) ---
	// System sets Gate4ai-User-Id
	// Let's imagine Server/Sub tried to set it too (though UI prevents this for system keys)
	// System should always win.
	assert.Equal(t, subscriber.ID, normalizedReceived["gate4ai-user-id"], "System header Gate4ai-User-Id was overridden")

	t.Log("E2E Header test completed successfully. Received headers:")
	for k, v := range receivedHeaders {
		t.Logf("  %s: %s", k, v)
	}
}
