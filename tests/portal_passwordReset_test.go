package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

func TestPasswordResetWithEmailEnabled(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	user, err := createUser(am) // Use the helper to register and confirm
	require.NoError(t, err, "Failed to create user for password reset test")
	require.NotEmpty(t, user.Email)
	require.NotEmpty(t, user.Password)
	t.Logf("User created: %s", user.Email)
	testStartTime := time.Now() // Record time *after* user creation

	// --- Setup: Create User and Enable Email ---
	require.NoError(t, updateSetting("email_do_not_send_email", false), "Failed to enable email sending")
	deleteAllMailHogMessages(t)

	// --- Action 1: Request Password Reset ---
	am.OpenPageWithURL("/forgot-password")

	_, err = am.WaitForLocatorWithDebug("input[type='email']", "forgot_email_input_wait")
	require.NoError(t, err, "Email input field did not appear on forgot password page")

	// Fill email and submit
	require.NoError(t, am.FillWithDebug("input[type='email']", user.Email, "forgot_email_input"), "Failed to fill email")
	require.NoError(t, am.ClickWithDebug("button[type='submit']:has-text('Send Reset Link')", "send_reset_link_button"), "Failed to click send reset link")

	// --- Verification 1: Check for success message ---
	successLocator := am.Page.Locator(fmt.Sprintf("text=Password reset instructions have been sent to %s", user.Email))
	err = successLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(10000)})
	require.NoError(t, err, "Did not find password reset sent confirmation message")
	t.Log("Found 'instructions sent' message.")
	am.SaveScreenshot("forgot_password_sent")

	// --- Verification 2: Check MailHog for Reset Email ---
	t.Logf("Checking MailHog for password reset email to %s", user.Email)
	emails, err := fetchEmailsFromMailHog(t, user.Email, testStartTime, 30*time.Second)
	require.NoError(t, err, "Failed to fetch password reset email from MailHog")
	require.Len(t, emails, 1, "Expected exactly one password reset email")
	resetEmail := emails[0]
	require.Contains(t, resetEmail.Content.Headers["Subject"][0], "Reset your gate4.ai password")

	// --- Action 2: Extract Reset Link and Visit ---
	linkPrefix := PORTAL_URL + "/reset-password/" // Use the determined portal URL
	resetLink, err := findLinkInEmail(t, resetEmail, linkPrefix)
	require.NoError(t, err, "Could not find password reset link in email")
	t.Logf("Found password reset link: %s", resetLink)

	am.OpenPageWithURL(resetLink)

	// --- Verification 3: Check Reset Password Page Loaded ---
	_, err = am.WaitForLocatorWithDebug("div.v-card-title:has-text('Set New Password')", "reset_password_heading")
	require.NoError(t, err, "Reset password page did not load")
	t.Log("Reset password page loaded.")
	am.SaveScreenshot("reset_password_page")

	// --- Action 3: Set New Password ---
	newPassword := user.Password + "-RESET"
	require.NoError(t, am.FillWithDebug("input[type='password'] >> nth=0", newPassword, "new_password_input"), "Failed to fill new password")
	require.NoError(t, am.FillWithDebug("input[type='password'] >> nth=1", newPassword, "confirm_password_input"), "Failed to fill confirm password")
	require.NoError(t, am.ClickWithDebug("button[type='submit']:has-text('Reset Password')", "reset_password_submit_button"), "Failed to click reset password submit")

	// --- Verification 4: Check for Success Message/Redirect ---
	successLocator = am.Page.Locator(".v-card:has-text('Password Reset Successful')") // More specific selector
	err = successLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(10000)})
	require.NoError(t, err, "Did not find password reset success message on page")
	t.Log("Password reset successful message found.")
	am.SaveScreenshot("reset_password_success")

	// --- Verification 5: Try logging in with the NEW password ---
	t.Logf("Attempting login with new password for %s", user.Email)
	// Ensure navigation away from reset page if needed, e.g., click "Go to Login"
	err = am.ClickWithDebug("a:has-text('Go to Login')", "go_to_login_button")
	require.NoError(t, err, "Failed to click 'Go to Login' after reset")
	err = loginUser(am, user.Email, newPassword) // Use helper with NEW password
	require.NoError(t, err, "Failed to login with new password")
	t.Log("Successfully logged in with new password.")
	am.SaveScreenshot("login_after_reset_success")

	// --- Verification 6: Try logging in with the OLD password (should fail) ---
	t.Logf("Attempting login with OLD password for %s (should fail)", user.Email)
	// Need to navigate back to login or ensure we are logged out first if necessary
	am.OpenPageWithURL("/login")                   // Go to login page
	err = loginUser(am, user.Email, user.Password) // Use helper with OLD password
	require.Error(t, err, "Login with old password should have failed")
	// Optionally check for specific error message on login page
	_, err = am.WaitForLocatorWithDebug(".v-snackbar__content:has-text('Invalid email or password')", "invalid_login_alert")
	require.NoError(t, err, "Expected 'Invalid email or password' alert after failed login with old password")
	t.Log("Verified login with old password failed.")
	am.SaveScreenshot("login_with_old_password_fail")
}

func TestPasswordResetWithEmailDisabled(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// --- Setup: Create User and Disable Email ---
	err := updateSetting("email_do_not_send_email", true)
	require.NoError(t, err, "Failed to disable email sending")

	user, err := createUser(am) // User is created as ACTIVE
	require.NoError(t, err, "Failed to create user")
	t.Logf("User created: %s", user.Email)

	// --- Action: Visit Forgot Password Page ---
	am.OpenPageWithURL("/forgot-password")

	// --- Verification: Check for Disabled Message ---
	disabledMessageLocator := am.Page.Locator("p:has-text('Password reset via email is currently disabled')")
	err = disabledMessageLocator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(5000)})
	require.NoError(t, err, "Did not find email disabled message on forgot password page")

	// Verify the form elements are NOT present
	emailInput := am.Page.Locator("input[type='email']")
	isVisible, _ := emailInput.IsVisible()
	require.False(t, isVisible, "Email input should NOT be visible when email is disabled")

	sendButton := am.Page.Locator("button:has-text('Send Reset Link')")
	isVisible, _ = sendButton.IsVisible()
	require.False(t, isVisible, "Send button should NOT be visible when email is disabled")

	t.Log("Verified forgot password page shows disabled message and hides form.")
	am.SaveScreenshot("forgot_password_disabled")
}
