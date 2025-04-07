<template>
  <v-row justify="center" align="center" class="auth-container">
    <v-col cols="12" sm="8" md="6" lg="4">
      <v-card class="pa-4">
        <v-card-title class="text-center text-h4 mb-4">
          Reset Password
        </v-card-title>

        <!-- Show if email is ENABLED -->
        <div v-if="!isEmailDisabled">
          <v-form ref="form" @submit.prevent="requestPasswordReset">
            <v-card-text>
               <p v-if="!requestSent" class="text-body-1 mb-4">
                Enter your email address and we'll send you a link to reset your password.
              </p>

              <v-alert v-if="errorMessage" type="error" class="mb-4" density="compact">
                {{ errorMessage }}
              </v-alert>

              <div v-if="!requestSent">
                 <v-text-field
                    v-model="email"
                    label="Email"
                    type="email"
                    required
                    :rules="[rules.required, rules.email]"
                    variant="outlined"
                    prepend-inner-icon="mdi-email"
                    class="mb-4"
                    :disabled="isLoading"
                  />

                  <v-btn
                    type="submit"
                    color="primary"
                    block
                    size="large"
                    :loading="isLoading"
                    class="mb-4"
                  >
                    Send Reset Link
                  </v-btn>
              </div>

               <!-- Success Message -->
              <div v-if="requestSent" class="text-center">
                 <v-icon color="success" size="x-large" class="mb-4">mdi-email-check-outline</v-icon>
                 <p class="text-body-1">
                   Password reset instructions have been sent to <strong>{{ email }}</strong>. Please check your inbox (and spam folder).
                 </p>
                  <v-btn
                    variant="text"
                    color="primary"
                    to="/login"
                    class="mt-4"
                  >
                    Back to Login
                  </v-btn>
              </div>

               <!-- Back to Login (when form is visible) -->
               <div v-if="!requestSent" class="text-center mt-2">
                  <v-btn
                    variant="text"
                    color="grey-darken-1"
                    to="/login"
                  >
                    Cancel
                  </v-btn>
                </div>

            </v-card-text>
          </v-form>
        </div>

        <!-- Show if email is DISABLED -->
        <div v-else class="pa-4 text-center">
           <v-icon color="warning" size="x-large" class="mb-4">mdi-email-off-outline</v-icon>
           <p class="text-body-1 mb-4">
             Password reset via email is currently disabled.
           </p>
           <p class="text-body-2">
             Please contact the administrators for assistance. If you are an admin, check the 'Email' settings.
           </p>
            <v-btn
                color="primary"
                to="/login"
                class="mt-6"
             >
               Back to Login
             </v-btn>
        </div>

      </v-card>
    </v-col>
  </v-row>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { rules } from '~/utils/validation';
import { useSnackbar } from '~/composables/useSnackbar';

definePageMeta({
  title: 'Forgot Password',
  layout: 'public', // Use a layout without login requirements
});

const { $settings, $api } = useNuxtApp(); // Access settings and api plugins
const { showError } = useSnackbar();

const email = ref('');
const isLoading = ref(false);
const requestSent = ref(false);
const errorMessage = ref('');
const form = ref<{ validate: () => Promise<{ valid: boolean }> } | null>(null);

// Compute email disabled status based on setting
const isEmailDisabled = computed(() => {
  // Access the setting. Default to 'true' (disabled) if not loaded or not boolean
  const settingValue = $settings.get('email_do_not_send_email');
  return !(settingValue === false);
});

onMounted(() => {
  // Check if settings are loaded, potentially trigger reload if needed
  if (!$settings.isLoaded()) {
      console.warn("Settings not loaded on forgot password page mount. Email disabled message might be shown initially.");
      // Consider triggering a reload, but be mindful of potential loops if loading fails
      // $settings.reload();
  }
});

async function requestPasswordReset() {
  errorMessage.value = ''; // Clear previous errors
  if (!form.value) return;

  const { valid } = await form.value.validate();
  if (!valid) return;

  isLoading.value = true;
  try {
    // Call the new backend API endpoint
    await $api.postJson('/auth/forgot-password', { email: email.value });
    requestSent.value = true; // Show success message
  } catch (err: unknown) {
    if (err instanceof Error) {
      errorMessage.value = err.message; // Display API error message
    } else {
       errorMessage.value = 'An unknown error occurred.';
    }
    showError(errorMessage.value); // Also show in snackbar
    console.error("Password reset request error:", err);
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