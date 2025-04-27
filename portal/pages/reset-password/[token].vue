<template>
  <v-row justify="center" align="center" class="auth-container">
    <v-col cols="12" sm="8" md="6" lg="4">
      <v-card class="pa-4">
        <v-card-title class="text-center text-h4 mb-4">
          Set New Password
        </v-card-title>

        <!-- Form visible before success -->
        <v-form
          v-if="!resetSuccess"
          ref="form"
          @submit.prevent="handlePasswordReset"
        >
          <v-card-text>
            <v-alert
              v-if="errorMessage"
              type="error"
              class="mb-4"
              density="compact"
            >
              {{ errorMessage }}
            </v-alert>

            <v-text-field
              v-model="newPassword"
              label="New Password"
              :type="showPassword ? 'text' : 'password'"
              required
              :rules="[rules.required, rules.password]"
              variant="outlined"
              prepend-inner-icon="mdi-lock"
              :append-inner-icon="showPassword ? 'mdi-eye-off' : 'mdi-eye'"
              class="mb-4"
              :disabled="isLoading"
              @click:append-inner="showPassword = !showPassword"
            />

            <v-text-field
              v-model="confirmPassword"
              label="Confirm New Password"
              :type="showPassword ? 'text' : 'password'"
              required
              :rules="[
                rules.required,
                (v) => v === newPassword || 'Passwords do not match',
              ]"
              variant="outlined"
              prepend-inner-icon="mdi-lock-check"
              :append-inner-icon="showPassword ? 'mdi-eye-off' : 'mdi-eye'"
              class="mb-4"
              :disabled="isLoading"
              @click:append-inner="showPassword = !showPassword"
            />

            <v-btn
              type="submit"
              color="primary"
              block
              size="large"
              :loading="isLoading"
              class="mb-4"
            >
              Reset Password
            </v-btn>
          </v-card-text>
        </v-form>

        <!-- Success Message -->
        <div v-else class="pa-4 text-center">
          <v-icon color="success" size="x-large" class="mb-4"
            >mdi-check-circle-outline</v-icon
          >
          <p class="text-h6 mb-4">Password Reset Successful!</p>
          <v-btn color="primary" to="/login" class="mt-4"> Go to Login </v-btn>
        </div>
      </v-card>
    </v-col>
  </v-row>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { useRoute } from "vue-router";
import { rules } from "~/utils/validation";
import { useSnackbar } from "~/composables/useSnackbar";

definePageMeta({
  title: "Reset Password",
  layout: "public", // Use a layout without login requirements
});

const route = useRoute();
const { $api } = useNuxtApp();
const { showError, showSuccess } = useSnackbar(); // Using snackbar

const resetToken = ref(route.params.token as string); // Get token from URL
const newPassword = ref("");
const confirmPassword = ref("");
const showPassword = ref(false);
const isLoading = ref(false);
const resetSuccess = ref(false);
const errorMessage = ref("");
const form = ref<{ validate: () => Promise<{ valid: boolean }> } | null>(null);

async function handlePasswordReset() {
  errorMessage.value = ""; // Clear previous errors
  if (!form.value) return;

  const { valid } = await form.value.validate();
  if (!valid) return;

  if (!resetToken.value) {
    errorMessage.value = "Invalid reset link.";
    showError(errorMessage.value);
    return;
  }

  isLoading.value = true;
  try {
    await $api.postJson("/auth/reset-password", {
      token: resetToken.value,
      newPassword: newPassword.value,
    });
    resetSuccess.value = true; // Show success message
    showSuccess("Password reset successfully!"); // Show snackbar success
  } catch (err: unknown) {
    if (err instanceof Error) {
      errorMessage.value = err.message; // Display API error message
    } else {
      errorMessage.value = "An unknown error occurred.";
    }
    showError(errorMessage.value); // Show error snackbar
    console.error("Password reset error:", err);
  } finally {
    isLoading.value = false;
  }
}
</script>

<style scoped>
.auth-container {
  min-height: calc(100vh - 200px);
}
</style>
