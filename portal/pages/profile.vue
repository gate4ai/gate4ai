<template>
  <v-row justify="center">
    <v-col cols="12" md="8">
      <v-card class="pa-4">
        <v-card-title class="text-h4 mb-4">User Profile</v-card-title>

        <v-card-text>
          <v-form ref="form" @submit.prevent="updateProfile">
            <v-text-field
              v-model="profile.name"
              label="Full Name"
              required
              :rules="[rules.required]"
              variant="outlined"
              class="mb-4"
              data-testid="profile-name-input"
            />

            <v-text-field
              v-model="profile.email"
              label="Email"
              type="email"
              required
              :rules="[rules.required, rules.email]"
              variant="outlined"
              class="mb-4"
              disabled
              data-testid="profile-email-input"
            />

            <v-text-field
              v-model="profile.company"
              label="Company"
              variant="outlined"
              class="mb-4"
              data-testid="profile-company-input"
            />

            <v-divider class="my-4" />

            <v-card-title class="text-h5 mb-2">Change Password</v-card-title>
            <v-text-field
              v-model="passwords.current"
              label="Current Password"
              type="password"
              variant="outlined"
              class="mb-4"
              :rules="passwords.current ? [rules.required] : []"
              data-testid="profile-current-password-input"
            />

            <v-text-field
              v-model="passwords.new"
              label="New Password"
              type="password"
              variant="outlined"
              class="mb-4"
              :rules="passwords.current ? [rules.required, rules.password] : []"
              data-testid="profile-new-password-input"
            />

            <v-text-field
              v-model="passwords.confirm"
              label="Confirm New Password"
              type="password"
              variant="outlined"
              class="mb-4"
              :rules="
                passwords.current
                  ? [
                      rules.required,
                      (v) => v === passwords.new || 'Passwords must match',
                    ]
                  : []
              "
              data-testid="profile-confirm-password-input"
            />

            <v-btn
              type="submit"
              color="primary"
              size="large"
              :loading="isLoading"
              class="mt-4"
              data-testid="profile-update-button"
            >
              Update Profile
            </v-btn>
          </v-form>
        </v-card-text>
      </v-card>
    </v-col>
  </v-row>
</template>

<script setup lang="ts">
import { ref, onMounted } from "vue";
import { rules } from "~/utils/validation";
import { useSnackbar } from "~/composables/useSnackbar";

definePageMeta({
  middleware: ["auth"],
  title: "Profile",
  layout: "default",
});

const { $auth } = useNuxtApp();

interface ProfileData {
  name: string;
  email: string;
  company: string;
}

interface PasswordData {
  current: string;
  new: string;
  confirm: string;
}

// Define UpdateData as a Record<string, unknown> to match updateProfile parameter type
interface UpdateData extends Record<string, unknown> {
  name: string;
  company: string;
  currentPassword?: string;
  newPassword?: string;
}

const profile = ref<ProfileData>({
  name: "",
  email: "",
  company: "",
});

const passwords = ref<PasswordData>({
  current: "",
  new: "",
  confirm: "",
});

const { showError, showSuccess } = useSnackbar();
const message = ref("");
const messageType = ref<"success" | "error" | "info" | "warning">("success");
const isLoading = ref(false);
const form = ref<{ validate: () => Promise<{ valid: boolean }> } | null>(null);

// Load user data on mount
onMounted(async () => {
  try {
    isLoading.value = true;

    // Get current user from auth service
    const userData = $auth.getUser();

    if (userData) {
      profile.value = {
        name: userData.name || "",
        email: userData.email || "",
        company: userData.company || "",
      };
    }
  } catch (error) {
    console.error("Failed to load profile:", error);
    message.value = "Failed to load profile data.";
    messageType.value = "error";
  } finally {
    isLoading.value = false;
  }
});

async function updateProfile() {
  if (!form.value) return;

  const { valid } = await form.value.validate();
  if (!valid) return;

  isLoading.value = true;
  message.value = "";

  try {
    // Prepare data to update
    const updateData: UpdateData = {
      name: profile.value.name,
      company: profile.value.company,
    };

    // Add password change if requested
    if (
      passwords.value.current &&
      passwords.value.new &&
      passwords.value.confirm
    ) {
      // Ensure all fields are provided
      updateData.currentPassword = passwords.value.current;
      updateData.newPassword = passwords.value.new;
    }

    // Use auth service to update profile
    await $auth.updateProfile(updateData);

    // Reset password fields after successful update
    passwords.value.current = "";
    passwords.value.new = "";
    passwords.value.confirm = "";

    showSuccess("Profile updated successfully");
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError("Failed to update profile");
    }
    console.error("Error updating profile:", err);
  } finally {
    isLoading.value = false;
  }
}
</script>
