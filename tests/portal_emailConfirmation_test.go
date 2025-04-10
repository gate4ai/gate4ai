package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

func TestUserRegistrationWithEmailConfirmation(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// --- Setup: Enable Email Sending ---
	err := updateSetting("email_do_not_send_email", false)
	require.NoError(t, err, "Failed to enable email sending")
	deleteAllMailHogMessages(t) // Clear previous emails
	testStartTime := time.Now() // Record time before registration

	// --- Action: Register User ---
	timestamp := time.Now().UnixNano()
	userEmail := fmt.Sprintf("confirm.%d@example.com", timestamp)
	userName := fmt.Sprintf("Confirm User %d", timestamp)
	userPassword := fmt.Sprintf("Password%d!", timestamp)

	am.OpenPageWithURL("/register")
	require.NoError(t, am.Page.Locator("input[type='text']").First().Fill(userName))
	require.NoError(t, am.Page.Locator("input[type='email']").Fill(userEmail))
	require.NoError(t, am.Page.Locator("input[type='password']").First().Fill(userPassword))
	require.NoError(t, am.Page.Locator("input[type='password']").Last().Fill(userPassword))
	require.NoError(t, am.Page.Locator("input[type='checkbox']").Check())
	require.NoError(t, am.Page.Locator("button[type='submit']").Click())

	// --- Verification 1: Check for success message ---
	// Expect a message asking user to check email, NOT redirect to /servers
	successLocator := am.Page.Locator(".v-alert:has-text('check your email'), .v-snackbar:has-text('check your email')") // Check alerts or snackbars
	if err := successLocator.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000),
	}); err != nil {
		am.SaveScreenshot("registration_confirm_message_fail")
		am.SaveHTML("registration_confirm_message_fail")
		t.Fatalf("Did not find confirmation prompt message after registration: %v", err)
	}
	t.Log("Found 'check email' message after registration.")

	// --- Verification 2: Check MailHog for Confirmation Email ---
	t.Logf("Checking MailHog for confirmation email to %s", userEmail)
	emails, err := fetchEmailsFromMailHog(t, userEmail, testStartTime, 30*time.Second)
	require.NoError(t, err, "Failed to fetch emails from MailHog")
	require.Len(t, emails, 1, "Expected exactly one confirmation email")
	confirmationEmail := emails[0]
	require.Contains(t, confirmationEmail.Content.Headers["Subject"][0], "Confirm your gate4.ai email")

	// --- Verification 3: Extract and Visit Confirmation Link ---
	// Confirmation link prefix should match `url_how_users_connect_to_the_portal` setting
	linkPrefix := PORTAL_URL + "/confirm-email/" // Use the determined portal URL
	confirmationLink, err := findLinkInEmail(t, confirmationEmail, linkPrefix)
	require.NoError(t, err, "Could not find confirmation link in email")
	t.Logf("Found confirmation link: %s", confirmationLink)

	// Visit the confirmation link
	am.OpenPageWithURL(confirmationLink)

	// --- Verification 4: Check for redirect to login with success query ---
	// The backend should redirect to /login?confirmed=true
	require.NoError(t, am.Page.WaitForURL("**/login**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000), // Wait for backend redirect
	}), "Page did not redirect to login after confirmation")
	t.Log("Successfully redirected to login page after confirmation.")
	am.SaveScreenshot("after_email_confirmation_redirect")

	// --- Verification 5: Try logging in with confirmed user ---
	err = loginUser(am, userEmail, userPassword) // Use the login helper
	require.NoError(t, err, "Failed to login with confirmed user")
	t.Log("Successfully logged in with confirmed user.")
	am.SaveScreenshot("login_after_confirmation_success")
}

func TestUserRegistrationEmailDisabled(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// --- Setup: Disable Email Sending ---
	err := updateSetting("email_do_not_send_email", true)
	require.NoError(t, err, "Failed to disable email sending")
	deleteAllMailHogMessages(t)
	testStartTime := time.Now()

	// --- Action: Register User ---
	timestamp := time.Now().UnixNano()
	userEmail := fmt.Sprintf("noconfirm.%d@example.com", timestamp)
	userName := fmt.Sprintf("No Confirm User %d", timestamp)
	userPassword := fmt.Sprintf("Password%d!", timestamp)

	am.OpenPageWithURL("/register")
	require.NoError(t, am.Page.Locator("input[type='text']").First().Fill(userName))
	require.NoError(t, am.Page.Locator("input[type='email']").Fill(userEmail))
	require.NoError(t, am.Page.Locator("input[type='password']").First().Fill(userPassword))
	require.NoError(t, am.Page.Locator("input[type='password']").Last().Fill(userPassword))
	require.NoError(t, am.Page.Locator("input[type='checkbox']").Check())
	require.NoError(t, am.Page.Locator("button[type='submit']").Click())

	// --- Verification 1: Check for redirect to /servers ---
	// User should be logged in immediately
	require.NoError(t, am.Page.WaitForURL("**/servers"), "Should redirect directly to servers page when email is disabled")
	t.Log("Successfully registered and redirected to servers page (email disabled).")
	am.SaveScreenshot("registration_email_disabled_success")

	// --- Verification 2: Check MailHog (should be empty) ---
	t.Logf("Checking MailHog for confirmation email to %s (should be none)", userEmail)
	emails, err := fetchEmailsFromMailHog(t, userEmail, testStartTime, 5*time.Second)       // Shorter timeout
	require.Error(t, err, "Expected an error (timeout) when fetching emails, but got none") // Expect timeout
	require.Empty(t, emails, "No confirmation email should have been sent")
	t.Log("Verified no confirmation email was sent.")

}
