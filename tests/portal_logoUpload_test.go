package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// Function to create a dummy image file for testing uploads
func createDummyImageFile(t *testing.T, filename string, content string, size int) string {
	t.Helper()
	if content == "" {
		content = fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="%s" /></svg>`, time.Now().Format("150405"))
	}
	if size > 0 && len(content) < size {
		padding := strings.Repeat(" ", size-len(content))
		content += padding
	}
	path := filepath.Join(t.TempDir(), filename)
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err, "Failed to create dummy image file")
	t.Logf("Created dummy file for testing: %s", path)
	return path
}

// TestLogoUpload tests the logo upload functionality
func TestLogoUpload(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// 1. Setup: Create user and server
	owner, err := createUser(am)
	require.NoError(t, err, "Failed to create owner user")
	server, err := addMCPServer(am, owner, "test-logo-upload-server")
	require.NoError(t, err, "Failed to add server")
	require.NotNil(t, server)

	// Ensure we are logged in as the owner
	require.NoError(t, loginUser(am, owner.Email, owner.Password), "Login failed")

	// 2. Navigate to Server Details Page
	serverDetailURL := fmt.Sprintf("/servers/%s", server.Slug)
	am.OpenPageWithURL(serverDetailURL)
	initialImgLocator := am.Page.Locator(fmt.Sprintf("img[alt='%s logo']", server.Name))
	initialImgSrc, err := initialImgLocator.GetAttribute("src")
	require.NoError(t, err, "Failed to get initial image src")
	require.True(t, strings.HasSuffix(initialImgSrc, "/images/default-server.svg"), "Initial image should be the default one")
	t.Logf("Initial image source verified: %s", initialImgSrc)

	// 3. Create a dummy image file for upload
	dummyImagePath := createDummyImageFile(t, "test-logo.svg", "", 0)

	// 4. Wait for and Click the Upload Button using data-testid
	uploadButtonSelector := "button[data-testid='upload-logo-button']"
	uploadButton, err := am.WaitForLocatorWithDebug(uploadButtonSelector, "wait_for_upload_button", 15000)
	require.NoError(t, err, "Upload button did not appear on server details page")

	// --- Robust Click Attempt ---
	t.Log("Attempting robust click on upload button...")
	// Ensure the button is enabled and visible before clicking
	err = uploadButton.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	})
	require.NoError(t, err, "Upload button did not become visible")
	isDisabled, err := uploadButton.IsDisabled()
	require.NoError(t, err, "Failed to check if upload button is disabled")
	require.False(t, isDisabled, "Upload button is unexpectedly disabled before click")

	// Try clicking directly
	err = uploadButton.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(5000)})
	if err != nil {
		t.Logf("Direct click failed (%v), trying to dispatch event...", err)
		// Fallback: Dispatch event
		err = uploadButton.DispatchEvent("click", playwright.LocatorDispatchEventOptions{Timeout: playwright.Float(5000)})
		require.NoError(t, err, "Failed to click upload button using dispatchEvent")
	}
	t.Log("Upload button clicked.")
	// --- End Robust Click Attempt ---


	// 5. Open the Upload Dialog
	dialogSelector := ".v-dialog:visible:has-text('Upload Server Logo')"
	dialog, err := am.WaitForLocatorWithDebug(dialogSelector, "upload_logo_dialog", 5000)
	require.NoError(t, err, "Upload logo dialog did not appear after clicking button")
	am.SaveScreenshot("logo_upload_dialog_opened")

	// 6. Select the File
	fileInputSelector := "input[type='file']"
	fileInput := dialog.Locator(fileInputSelector)
	err = fileInput.SetInputFiles(dummyImagePath)
	require.NoError(t, err, "Failed to set input file")
	t.Logf("Selected file: %s", dummyImagePath)

	// 7. Verify Preview (Optional but good)
	previewImage := dialog.Locator("img[alt='Logo preview']")
	err = previewImage.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(5000)})
	require.NoError(t, err, "Logo preview did not appear")
	previewSrc, _ := previewImage.GetAttribute("src")
	require.True(t, strings.HasPrefix(previewSrc, "blob:"), "Preview src should be a blob URL")
	t.Logf("Logo preview is visible: %s", previewSrc)
	am.SaveScreenshot("logo_upload_dialog_preview")

	// 8. Click Upload
	uploadDialogButton := dialog.Locator("button:has-text('Upload')")
	// Ensure dialog button is enabled before clicking
	isDisabledDialog, err := uploadDialogButton.IsDisabled()
	require.NoError(t, err, "Failed to check if dialog upload button is disabled")
	require.False(t, isDisabledDialog, "Dialog upload button is unexpectedly disabled")
	require.NoError(t, uploadDialogButton.Click(), "Failed to click upload button in dialog")


	// 9. Wait for Dialog to Close & Success Notification
	err = dialog.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateHidden, Timeout: playwright.Float(10000)})
	require.NoError(t, err, "Upload dialog did not close")

	_, err = am.WaitForLocatorWithDebug(".v-snackbar:has-text('Logo uploaded successfully!')", "upload_success_snackbar", 10000)
	require.NoError(t, err, "Success snackbar did not appear")
	t.Log("Logo uploaded successfully")

	// 10. Verify Image Source Updated on Page
	time.Sleep(500 * time.Millisecond)
	updatedImgLocator := am.Page.Locator(fmt.Sprintf("img[alt='%s logo']", server.Name))
	var updatedImgSrc string
	for i := 0; i < 5; i++ {
		updatedImgSrc, err = updatedImgLocator.GetAttribute("src")
		if err == nil && strings.Contains(updatedImgSrc, "/uploads/servers/") && strings.Contains(updatedImgSrc, server.Slug) {
			break
		}
		t.Logf("Retry %d: Waiting for image src update...", i+1)
		time.Sleep(300 * time.Millisecond)
	}
	require.NoError(t, err, "Failed to get updated image src")
	require.True(t, strings.HasPrefix(updatedImgSrc, "/uploads/servers/"), "Image src should start with /uploads/servers/")
	require.True(t, strings.Contains(updatedImgSrc, server.Slug), "Image src should contain the server slug")
	require.True(t, strings.HasSuffix(updatedImgSrc, ".svg"), "Image src should have the correct extension")
	t.Logf("Image source updated on page: %s", updatedImgSrc)
	am.SaveScreenshot("logo_upload_success_page_updated")

	// 11. Reload Page and Verify Persistence
	am.T.Logf("Reloading page to verify persistence...")
	_, err = am.Page.Reload(playwright.PageReloadOptions{WaitUntil: playwright.WaitUntilStateNetworkidle})
	require.NoError(t, err, "Failed to reload page")

	persistentImgLocator := am.Page.Locator(fmt.Sprintf("img[alt='%s logo']", server.Name))
	persistentImgSrc, err := persistentImgLocator.GetAttribute("src")
	require.NoError(t, err, "Failed to get image src after reload")
	require.Equal(t, updatedImgSrc, persistentImgSrc, "Image src mismatch after reload")
	t.Logf("Image persistence verified after reload: %s", persistentImgSrc)
}
