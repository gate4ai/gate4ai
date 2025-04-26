package tests

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Helper function to navigate to profile page and wait for load
func navigateToProfile(am *ArtifactManager) error {
	am.OpenPageWithURL("/profile")
	// Wait for a specific element known to be on the profile page, like the form or a data-testid
	// REMOVED: profileForm := am.Page.Locator("form") // No need to assign if only used for waiting indirectly
	_, err := am.WaitForLocatorWithDebug("[data-testid='profile-name-input']", "profile_name_field_wait") // Wait for a specific field to ensure form is ready
	if err != nil {
		am.SaveScreenshot("profile_page_load_fail")
		return fmt.Errorf("profile page form did not load: %w", err)
	}
	am.SaveScreenshot("profile_page_loaded")
	return nil
}

// Helper function to update basic profile info (name, company)
func updateBasicProfileInfo(am *ArtifactManager, newName, newCompany string) error {
	am.T.Logf("Updating profile: Name=%s, Company=%s", newName, newCompany)

	// Use data-testid selectors
	nameFieldSelector := "[data-testid='profile-name-input'] input" // Target the input within
	companyFieldSelector := "[data-testid='profile-company-input'] input" // Target the input within
	submitButtonSelector := "[data-testid='profile-update-button']" // Use testid for button too

	// --- Update Name ---
	nameField, err := am.WaitForLocatorWithDebug(nameFieldSelector, "profile_name_field")
	if err != nil {
		return fmt.Errorf("could not find name field: %w", err)
	}
	require.NoError(am.T, nameField.Clear(), "Failed to clear name field")
	require.NoError(am.T, nameField.Fill(newName), "Failed to fill name field")

	// --- Update Company ---
	companyField, err := am.WaitForLocatorWithDebug(companyFieldSelector, "profile_company_field")
	if err != nil {
		return fmt.Errorf("could not find company field: %w", err)
	}
	require.NoError(am.T, companyField.Clear(), "Failed to clear company field")
	require.NoError(am.T, companyField.Fill(newCompany), "Failed to fill company field")

	am.SaveScreenshot("profile_form_filled_basic")

	// --- Submit ---
	if err := am.ClickWithDebug(submitButtonSelector, "profile_update_submit"); err != nil {
		return fmt.Errorf("failed to click update profile button: %w", err)
	}

	// --- Verify Success ---
	if _, err := am.WaitForLocatorWithDebug(".v-snackbar:has-text('Profile updated successfully')", "profile_update_success_snackbar"); err != nil {
		am.SaveScreenshot("profile_update_no_success_snackbar")
		return fmt.Errorf("profile update success message not found: %w", err)
	}
	am.T.Log("Profile updated successfully (basic info)")
	am.SaveScreenshot("profile_update_basic_success")

	// Verify fields retained updated values on the page
	updatedName, _ := nameField.InputValue()
	require.Equal(am.T, newName, updatedName, "Name field did not retain updated value")
	updatedCompany, _ := companyField.InputValue()
	require.Equal(am.T, newCompany, updatedCompany, "Company field did not retain updated value")

	return nil
}

// Helper function to attempt password change
func attemptPasswordChange(am *ArtifactManager, currentPassword, newPassword, confirmPassword string) error {
	am.T.Logf("Attempting password change: Current=***, New=***")

	// Use data-testid selectors, targeting the input element within the component
	currentPasswordFieldSelector := "[data-testid='profile-current-password-input'] input"
	newPasswordFieldSelector := "[data-testid='profile-new-password-input'] input"
	confirmPasswordFieldSelector := "[data-testid='profile-confirm-password-input'] input"
	submitButtonSelector := "[data-testid='profile-update-button']"

	// --- Fill Fields ---
	require.NoError(am.T, am.FillWithDebug(currentPasswordFieldSelector, currentPassword, "current_password_field"), "Failed to fill current password")
	require.NoError(am.T, am.FillWithDebug(newPasswordFieldSelector, newPassword, "new_password_field"), "Failed to fill new password")
	require.NoError(am.T, am.FillWithDebug(confirmPasswordFieldSelector, confirmPassword, "confirm_password_field"), "Failed to fill confirm password")

	am.SaveScreenshot("profile_form_filled_password")

	// --- Submit ---
	if err := am.ClickWithDebug(submitButtonSelector, "profile_update_submit_password"); err != nil {
		return fmt.Errorf("failed to click update profile button for password change: %w", err)
	}

	// Verification of success/failure happens in the calling test
	return nil
}

// Test updating own profile (name and company only)
func TestUpdateOwnProfile_Basic(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create user and log in
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")
	require.NoError(t, loginUser(am, user.Email, user.Password), "Login failed")

	// Navigate to profile
	require.NoError(t, navigateToProfile(am), "Failed to navigate to profile")

	// Update profile info
	newName := "Updated Name " + am.Timestamp
	newCompany := "Updated Company " + am.Timestamp
	require.NoError(t, updateBasicProfileInfo(am, newName, newCompany), "Failed to update basic profile info")
}

// Test changing own password successfully
func TestUpdateOwnProfile_PasswordSuccess(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create user and log in
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")
	initialPassword := user.Password
	require.NoError(t, loginUser(am, user.Email, initialPassword), "Initial login failed")

	// Navigate to profile
	require.NoError(t, navigateToProfile(am), "Failed to navigate to profile")

	// Attempt password change
	newPassword := initialPassword + "_new"
	err = attemptPasswordChange(am, initialPassword, newPassword, newPassword)
	require.NoError(t, err, "Password change attempt failed unexpectedly")

	// Verify success message
	if _, err := am.WaitForLocatorWithDebug(".v-snackbar:has-text('Profile updated successfully')", "password_change_success_snackbar"); err != nil {
		am.SaveScreenshot("password_change_no_success_snackbar")
		t.Fatalf("Password change success message not found: %v", err)
	}
	am.T.Log("Password change successful")
	am.SaveScreenshot("profile_update_password_success")

	// --- Verify new password works ---
	am.T.Log("Verifying login with new password...")
	// Need to log out first
	am.OpenPageWithURL("/") // Navigate away
	// Adjust logout selector if needed
	logoutBtn, err := am.WaitForLocatorWithDebug("button:has-text('Logout')", "logout_button_wait_password")
	if err == nil { // Only click if found
		require.NoError(t, logoutBtn.Click(), "Failed to click logout button")
	} else {
		am.T.Logf("Could not find Logout button, attempting login anyway (might fail if still logged in)")
	}
	require.NoError(t, loginUser(am, user.Email, newPassword), "Login with NEW password failed")
	am.T.Log("Login with NEW password successful")
	am.SaveScreenshot("login_with_new_password_success")

	// --- Verify old password fails ---
	am.T.Log("Verifying login with old password fails...")
	am.OpenPageWithURL("/") // Navigate away
	logoutBtn, err = am.WaitForLocatorWithDebug("button:has-text('Logout')", "logout_button_wait_password_old")
	if err == nil { // Only click if found
		require.NoError(t, logoutBtn.Click(), "Failed to click logout button")
	} else {
		am.T.Logf("Could not find Logout button, attempting login anyway (might fail if still logged in)")
	}
	err = loginUser(am, user.Email, initialPassword) // Attempt with OLD password
	require.Error(t, err, "Login with OLD password should have failed")
	// Check for specific error message
	_, err = am.WaitForLocatorWithDebug(".v-snackbar__content:has-text('Invalid email or password')", "login_fail_old_password_snackbar")
	require.NoError(t, err, "Expected 'Invalid email or password' snackbar after failed login with old password")
	am.T.Log("Login with OLD password failed as expected")
	am.SaveScreenshot("login_with_old_password_fail")
}

// Test changing own password with incorrect current password
func TestUpdateOwnProfile_PasswordFail_IncorrectCurrent(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create user and log in
	user, err := createUser(am)
	require.NoError(t, err, "Failed to create user")
	require.NoError(t, loginUser(am, user.Email, user.Password), "Login failed")

	// Navigate to profile
	require.NoError(t, navigateToProfile(am), "Failed to navigate to profile")

	// Attempt password change with wrong current password
	newPassword := user.Password + "_new"
	err = attemptPasswordChange(am, "wrong_current_password", newPassword, newPassword)
	require.NoError(t, err, "Password change attempt function failed") // The helper itself shouldn't fail

	// Verify specific error message for incorrect password
	if _, err := am.WaitForLocatorWithDebug(".v-snackbar__content:has-text('Incorrect current password.')", "incorrect_password_snackbar"); err != nil {
		am.SaveScreenshot("password_change_no_error_snackbar")
		t.Fatalf("Expected 'Incorrect current password' error message not found: %v", err)
	}
	am.T.Log("Received expected error for incorrect current password")
	am.SaveScreenshot("profile_update_password_fail_incorrect")
}

// Test admin updating another user's profile (role, status, comment)
func TestAdminUpdateUserProfile(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create a regular user
	targetUser, err := createUser(am)
	require.NoError(t, err, "Failed to create target user")

	// Get admin credentials
	adminUser, err := getAdminUser()
	require.NoError(t, err, "Failed to get admin user credentials")

	// Log in as admin
	require.NoError(t, loginUser(am, adminUser.Email, adminUser.Password), "Admin login failed")

	// Navigate to users page
	am.OpenPageWithURL("/users")
	_, err = am.WaitForLocatorWithDebug("h1:has-text('User Management')", "user_management_heading_admin")
	require.NoError(t, err, "Failed to load user management page as admin")

	// Find and click the target user's row
	userRowSelector := fmt.Sprintf("tr:has-text('%s')", targetUser.Email)
	userRow, err := am.WaitForLocatorWithDebug(userRowSelector, "target_user_row")
	require.NoError(t, err, "Failed to find target user row")
	require.NoError(t, userRow.Click(), "Failed to click target user row")

	// Wait for user profile page to load (should be /users/[id])
	profileHeadingSelector := "div.v-card-title:has-text('User Profile')"
	_, err = am.WaitForLocatorWithDebug(profileHeadingSelector, "user_profile_heading_admin_view")
	require.NoError(t, err, "Failed to load target user profile page")
	am.SaveScreenshot("admin_view_user_profile")

	// --- Make changes (Role, Status, Comment) ---
	am.T.Log("Admin updating target user's Role, Status, and Comment...")

	// Change Role to DEVELOPER using data-testid
	roleDropdownSelector := "[data-testid='user-profile-role-select']"
	require.NoError(t, am.ClickWithDebug(roleDropdownSelector, "role_dropdown_click"), "Failed to click role dropdown")
	roleOptionSelector := "div.v-overlay__content .v-list-item-title:has-text('Developer')" // Look inside overlay
	require.NoError(t, am.ClickWithDebug(roleOptionSelector, "role_developer_option_click"), "Failed to click Developer role option")

	// Change Status to BLOCKED using data-testid
	statusDropdownSelector := "[data-testid='user-profile-status-select']"
	require.NoError(t, am.ClickWithDebug(statusDropdownSelector, "status_dropdown_click"), "Failed to click status dropdown")
	statusOptionSelector := "div.v-overlay__content .v-list-item-title:has-text('Blocked')"
	require.NoError(t, am.ClickWithDebug(statusOptionSelector, "status_blocked_option_click"), "Failed to click Blocked status option")

	// Add a comment - Use the new data-testid selector, targeting the textarea within
	commentFieldSelector := "[data-testid='admin-comment-textarea'] textarea" // Target textarea inside
	commentField, err := am.WaitForLocatorWithDebug(commentFieldSelector, "admin_comment_textarea")
	require.NoError(t, err, "Could not find comment textarea using data-testid")
	newComment := "Admin comment " + am.Timestamp
	require.NoError(t, commentField.Fill(newComment), "Failed to fill admin comment")

	am.SaveScreenshot("admin_update_form_filled")

	// --- Submit ---
	// Use testid added to the button
	submitButtonSelector := "[data-testid='user-profile-update-button']"
	require.NoError(t, am.ClickWithDebug(submitButtonSelector, "admin_update_submit"), "Failed to click update profile button as admin")

	// --- Verify Success ---
	_, err = am.WaitForLocatorWithDebug(".v-snackbar:has-text('User updated successfully')", "admin_update_success_snackbar")
	require.NoError(t, err, "Admin update success message not found")
	am.T.Log("Admin successfully updated user profile")
	am.SaveScreenshot("admin_update_success")

	// --- Verify Changes on Page ---
	// Role should show Developer chip (may need to adjust selector)
	roleChipSelector := ".v-chip:has-text('Developer')"
	_, err = am.WaitForLocatorWithDebug(roleChipSelector, "verify_role_developer_chip")
	require.NoError(t, err, "Role chip did not update to Developer")

	// Status should show Blocked chip
	statusChipSelector := ".v-chip:has-text('Blocked')"
	_, err = am.WaitForLocatorWithDebug(statusChipSelector, "verify_status_blocked_chip")
	require.NoError(t, err, "Status chip did not update to Blocked")

	// Comment field should retain value
	commentValue, err := am.Page.Locator(commentFieldSelector).InputValue() // Use the same selector to verify
	require.NoError(t, err, "Failed to get comment value after update")
	require.Equal(t, newComment, commentValue, "Admin comment did not persist")

	am.T.Log("Verified profile changes made by admin")
}