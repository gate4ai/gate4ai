package tests

import (
	"fmt"
	"testing"

	"github.com/playwright-community/playwright-go"
)

// getAdminUser creates a new admin user using the admin credentials from seed.ts
func getAdminUser() (*User, error) {
	// Create an admin user object with the credentials from seed.ts
	// Note: These need to match the admin user created in the seed script
	adminUser := &User{
		Email:    "admin@gate4.ai",
		Password: "Admin123!",
	}

	return adminUser, nil
}

// viewUserList tests viewing the user management page as an admin
func viewUserList(am *ArtifactManager, adminUser *User) error {
	am.T.Helper()

	// Log in as admin
	loginUser(am, adminUser.Email, adminUser.Password)

	// Navigate to users page
	am.OpenPageWithURL("/users")

	// Wait for the users page to load - match the exact h1 class from the HTML
	if _, err := am.WaitForLocatorWithDebug("h1.text-h3:has-text('User Management')", "user_management_heading"); err != nil {
		return fmt.Errorf("user management page did not load: %w", err)
	}

	// Check for search input with the exact placeholder from the HTML
	if _, err := am.WaitForLocatorWithDebug("input[id^='input-']", "search_input"); err != nil {
		return fmt.Errorf("search field not found: %w", err)
	}

	// Check if search has the correct label
	searchLabel := am.Page.Locator("label.v-label:has-text('Search users by name, email, or company')").First()
	labelVisible, err := searchLabel.IsVisible()
	if err != nil || !labelVisible {
		return fmt.Errorf("search field label not found: %w", err)
	}

	// Check for user table
	usersTable := am.Page.Locator(".v-table table").First()
	tableVisible, err := usersTable.IsVisible()
	if err != nil || !tableVisible {
		return fmt.Errorf("users table not found: %w", err)
	}

	// Check for table headers
	headers := []string{"Full Name", "Email", "Company", "Role", "Status"}
	for _, header := range headers {
		headerLocator := am.Page.Locator(fmt.Sprintf("thead th:has-text('%s')", header))
		headerVisible, err := headerLocator.IsVisible()
		if err != nil || !headerVisible {
			return fmt.Errorf("table header '%s' not found: %w", header, err)
		}
	}

	// Check if table has data by looking for rows with class "user-row"
	rowCount, err := am.Page.Locator("tbody tr.user-row").Count()
	if err != nil {
		return fmt.Errorf("could not count table rows: %w", err)
	}

	if rowCount == 0 {
		// Check for empty state
		emptyState := am.Page.Locator("text='No users found'").First()
		emptyStateVisible, _ := emptyState.IsVisible()
		if !emptyStateVisible {
			return fmt.Errorf("no users in table and no empty state message found")
		}
	} else {
		// Verify at least one user email is present
		emailCell := am.Page.Locator("tbody td a[href^='mailto:']").First()
		emailVisible, err := emailCell.IsVisible()
		if err != nil || !emailVisible {
			return fmt.Errorf("user email not found in table: %w", err)
		}
	}

	return nil
}

// viewUserProfile tests viewing and editing a user profile as an admin
func viewUserProfile(am *ArtifactManager, adminUser *User, targetUser *User) error {
	// Log in as admin
	loginUser(am, adminUser.Email, adminUser.Password)

	// Navigate to users page
	am.OpenPageWithURL("/users")

	// Wait for the users page to load
	if _, err := am.WaitForLocatorWithDebug("h1:has-text('User Management')", "user_management_heading"); err != nil {
		return fmt.Errorf("user management page did not load: %w", err)
	}

	// Search for the target user
	searchField := am.Page.Locator(".v-text-field input").First()
	if err := searchField.Fill(targetUser.Email); err != nil {
		return fmt.Errorf("could not fill search field: %w", err)
	}

	// Find and click on the user row containing the target user's email
	userRow := am.Page.Locator("tr.user-row").Filter(playwright.LocatorFilterOptions{
		Has: am.Page.Locator(fmt.Sprintf("a[href*='mailto:%s']", targetUser.Email)),
	})
	// Click on the first cell (name) in the row instead of the entire row or email cell
	firstCell := userRow.Locator("td").First()
	if err := firstCell.Click(); err != nil {
		return fmt.Errorf("could not click on user row first cell: %w", err)
	}

	// Wait for the user profile page to load
	if _, err := am.WaitForLocatorWithDebug(".v-card-title:has-text('User Profile'), h1:has-text('User Profile')", "user_profile_heading"); err != nil {
		return fmt.Errorf("user profile page did not load: %w", err)
	}

	// Verify admin-only fields are present
	adminSection := am.Page.Locator(".v-card-title:has-text('Administration'), h2:has-text('Administration')").First()
	adminSectionVisible, err := adminSection.IsVisible()
	if err != nil || !adminSectionVisible {
		return fmt.Errorf("admin section not found: %w", err)
	}

	// Change the user's role (select a different role)
	roleSelect := am.Page.Locator(".v-select:has(label:has-text('Role'))").First()
	if err := roleSelect.Click(); err != nil {
		return fmt.Errorf("could not click role select: %w", err)
	}

	// Wait for dropdown to appear and select a role
	if err := am.Page.Locator(".v-list-item").First().Click(); err != nil {
		return fmt.Errorf("could not select a role: %w", err)
	}

	// Submit the form
	updateProfileButton, err := am.WaitForLocatorWithDebug("button:has-text('Update Profile'), button:has-text('Save')", "update_profile_button")
	if err != nil {
		return fmt.Errorf("could not find update profile button: %w", err)
	}
	if err := updateProfileButton.Click(); err != nil {
		return fmt.Errorf("could not click update profile button: %w", err)
	}

	// Wait for success message
	if err := am.Page.Locator(".v-snackbar:has-text('updated'), .v-snackbar:has-text('success')").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		// Not finding the success message isn't necessarily fatal
		am.T.Logf("Warning: Success message not found after updating user profile")
	}

	return nil
}

func TestUserManagement(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	regularUser, err := createUser(am)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	adminUser, err := getAdminUser()
	if err != nil {
		t.Fatalf("Failed to get admin user: %v", err)
	}

	err = viewUserList(am, adminUser)
	if err != nil {
		t.Fatalf("Failed to view user list: %v", err)
	}

	t.Logf("Successfully viewed user list as admin")

	// Test viewing and editing the regular user's profile as admin
	err = viewUserProfile(am, adminUser, regularUser)
	if err != nil {
		t.Fatalf("Failed to view/edit user profile: %v", err)
	}

	t.Logf("Successfully viewed and edited user profile for: %s", regularUser.Email)
}
