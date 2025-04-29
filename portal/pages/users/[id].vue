<template>
  <v-row justify="center">
    <v-col cols="12" md="8">
      <v-card class="pa-4">
        <v-card-title class="d-flex align-center">
          <span class="text-h4 mr-4">User Profile</span>
          <v-chip v-if="isAdminOrSecurity && user.role" color="primary">
            {{ formatRole(user.role) }}
          </v-chip>
          <v-chip
            v-if="isAdminOrSecurity && user.status"
            :color="getStatusColor(user.status)"
            class="ml-2"
          >
            {{ formatStatus(user.status) }}
          </v-chip>
        </v-card-title>

        <v-card-text>
          <v-form ref="form" @submit.prevent="updateProfile">
            <v-text-field
              v-model="user.name"
              label="Full Name"
              required
              :rules="[rules.required]"
              variant="outlined"
              class="mb-4"
              data-testid="user-profile-name-input"
            />

            <v-text-field
              v-model="user.email"
              label="Email"
              type="email"
              required
              :rules="[rules.required, rules.email]"
              variant="outlined"
              class="mb-4"
              disabled
              data-testid="user-profile-email-input"
            />

            <v-text-field
              v-model="user.company"
              label="Company"
              variant="outlined"
              class="mb-4"
              data-testid="user-profile-company-input"
            />

            <!-- Admin-only fields -->
            <template v-if="isAdminOrSecurity">
              <v-divider class="my-4" />
              <v-card-title class="text-h5 mb-2">Administration</v-card-title>

              <v-select
                v-model="user.role"
                label="Role"
                :items="roleOptions"
                item-title="title"
                item-value="value"
                variant="outlined"
                class="mb-4"
                data-testid="user-profile-role-select"
              />

              <v-select
                v-model="user.status"
                label="Status"
                :items="statusOptions"
                item-title="title"
                item-value="value"
                variant="outlined"
                class="mb-4"
                data-testid="user-profile-status-select"
              />

              <v-textarea
                v-model="user.comment"
                label="Comment"
                variant="outlined"
                class="mb-4"
                rows="3"
                data-testid="admin-comment-textarea"
              />
            </template>

            <v-btn
              type="submit"
              color="primary"
              size="large"
              :loading="isLoading"
              class="mt-4"
              data-testid="user-profile-update-button"
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
import { ref, computed, onMounted } from "vue";
import { rules } from "~/utils/validation";
import { useSnackbar } from "~/composables/useSnackbar";
import type { Role, Status } from "@prisma/client";

definePageMeta({
  middleware: ["auth"],
  title: "User Profile",
  layout: "default",
});

const route = useRoute();
const userId = route.params.id;
const { $auth, $api } = useNuxtApp();

// Define a type for user data received/sent to API
// Use the imported types Role and Status here
interface UserData {
  id: string;
  name: string | null;
  email: string;
  company: string | null;
  role: Role; // Use Prisma type for type checking
  status: Status; // Use Prisma type for type checking
  comment: string | null;
}

// Type for the data payload sent in the PUT request
interface UpdateUserData {
  name?: string | null;
  company?: string | null;
  role?: Role;
  status?: Status;
  comment?: string | null;
  [key: string]: unknown;
}

// Use string literals for default values matching the enum definitions
const user = ref<UserData>({
  id: "",
  name: "",
  email: "",
  company: null,
  role: "USER", // Default Role as string literal
  status: "EMAIL_NOT_CONFIRMED", // Default Status as string literal
  comment: null,
});

const message = ref("");
const messageType = ref<"success" | "error" | "info" | "warning">("success");
const isLoading = ref(false);
const form = ref<{ validate: () => Promise<{ valid: boolean }> } | null>(null);

const { showError, showSuccess } = useSnackbar();

// Use string literals for options values
const roleOptions = [
  { title: "User", value: "USER" },
  { title: "Developer", value: "DEVELOPER" },
  { title: "Admin", value: "ADMIN" },
  { title: "Security", value: "SECURITY" },
];

// Use string literals for options values
const statusOptions = [
  { title: "Active", value: "ACTIVE" },
  { title: "Email not confirmed", value: "EMAIL_NOT_CONFIRMED" },
  { title: "Blocked", value: "BLOCKED" },
];

// Computed property to check if current user is admin or security
const isAdminOrSecurity = computed(() => {
  return $auth.isSecurityOrAdmin();
});

// Load user data on mount
onMounted(async () => {
  const currentUserId = $auth.getUser()?.id;
  if (!isAdminOrSecurity.value && currentUserId !== userId) {
    showError("Forbidden: You can only view your own profile.");
    navigateTo("/profile");
    return;
  }
  await fetchUserData();
});

async function fetchUserData() {
  try {
    isLoading.value = true;
    const userData = await $api.getJson<UserData>(`/users/${userId}`);
    user.value = {
      ...userData,
      name: userData.name ?? "",
      company: userData.company ?? "",
      comment: userData.comment ?? "",
    };
  } catch (error: unknown) {
    console.error("Failed to load user data:", error);
    message.value =
      error instanceof Error ? error.message : "Failed to load user data.";
    messageType.value = "error";
    showError(message.value);
  } finally {
    isLoading.value = false;
  }
}

async function updateProfile() {
  if (!form.value) return;
  const { valid } = await form.value.validate();
  if (!valid) return;

  isLoading.value = true;
  message.value = "";

  try {
    // Prepare data to update, sending string values
    const updateData: UpdateUserData = {
      name: user.value.name || null,
      company: user.value.company || null,
    };

    if (isAdminOrSecurity.value) {
      updateData.role = user.value.role;
      updateData.status = user.value.status;
      updateData.comment = user.value.comment || null;
    }

    const updatedUser = await $api.putJson<UserData>(
      `/users/${userId}`,
      updateData
    );
    showSuccess("User updated successfully");

    user.value = {
      ...updatedUser,
      name: updatedUser.name ?? "",
      company: updatedUser.company ?? "",
      comment: updatedUser.comment ?? "",
    };
  } catch (error: unknown) {
    showError(error instanceof Error ? error.message : "Failed to update user");
    console.error("Error updating user:", error);
  } finally {
    isLoading.value = false;
  }
}

// Formatting functions now accept the string type directly from the UserData interface
function formatRole(role: Role | string): string {
  // Accept string as well
  switch (role) {
    case "ADMIN":
      return "Admin";
    case "SECURITY":
      return "Security";
    case "USER":
      return "User";
    case "DEVELOPER":
      return "Developer";
    default:
      return role;
  }
}

function formatStatus(status: Status | string): string {
  // Accept string as well
  switch (status) {
    case "ACTIVE":
      return "Active";
    case "BLOCKED":
      return "Blocked";
    case "EMAIL_NOT_CONFIRMED":
      return "Email not confirmed";
    default:
      return status;
  }
}

function getStatusColor(status: Status | string): string {
  // Accept string as well
  switch (status) {
    case "ACTIVE":
      return "success";
    case "BLOCKED":
      return "error";
    case "EMAIL_NOT_CONFIRMED":
      return "warning";
    default:
      return "info";
  }
}
</script>
