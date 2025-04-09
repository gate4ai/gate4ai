<template>
  <v-row justify="center" align="center" class="auth-container">
    <v-col cols="12" sm="8" md="6" lg="4">
      <v-card class="pa-4">
        <v-card-title class="text-center text-h4 mb-4">
          Login to gate4.ai
        </v-card-title>
        
        <v-form ref="form" @submit.prevent="handleLogin">
          <v-card-text>
            <v-text-field
              v-model="email"
              label="Email"
              type="email"
              required
              :rules="[rules.required, rules.email]"
              variant="outlined"
              prepend-inner-icon="mdi-email"
              class="mb-4"
            />
            
            <v-text-field
              v-model="password"
              label="Password"
              :type="showPassword ? 'text' : 'password'"
              required
              :rules="[rules.required]"
              variant="outlined"
              prepend-inner-icon="mdi-lock"
              :append-inner-icon="showPassword ? 'mdi-eye-off' : 'mdi-eye'"
              @click:append-inner="showPassword = !showPassword"
            />
            
            <div class="d-flex justify-end mb-4">
              <v-btn
                variant="text"
                color="primary"
                size="small"
                to="/forgot-password"
              >
                Forgot Password?
              </v-btn>
            </div>
            
            <v-btn
              type="submit"
              color="primary"
              block
              size="large"
              :loading="isLoading"
              class="mb-4"
            >
              Login
            </v-btn>
            
            <div class="text-center">
              <span class="text-medium-emphasis">Don't have an account?</span>
              <v-btn
                variant="text"
                color="primary"
                to="/register"
                class="ml-2"
              >
                Register
              </v-btn>
            </div>
          </v-card-text>
        </v-form>
      </v-card>
    </v-col>
  </v-row>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import { rules } from '~/utils/validation';
import { useSnackbar } from '~/composables/useSnackbar';

definePageMeta({
  title: 'Login',
  layout: 'default',
});

const route = useRoute();
const { $auth } = useNuxtApp();
const redirectPath = route.query.redirect as string || '/servers';

const email = ref('');
const password = ref('');
const showPassword = ref(false);
const error = ref('');
const isLoading = ref(false);
const form = ref<{ validate: () => Promise<{ valid: boolean }> } | null>(null);
const { showError } = useSnackbar();

async function handleLogin() {
  if (!form.value) return;
  
  const { valid } = await form.value.validate();
  
  if (!valid) {
    console.log("Validation failed");
    return;
  }
  console.log("Form is valid, proceeding...");  

  isLoading.value = true;
  error.value = '';
  
  try {
    // Call the login API
    const { $api } = useNuxtApp();
    const response = await $api.postJson('/auth/login', {
      email: email.value,
      password: password.value
    });

    // Use the auth plugin to store token and handle login
    $auth.login(response.token, response.user);
    
    // Redirect
    navigateTo(redirectPath);
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('An error occurred during login. Please try again.');
    }
    console.error("Login error:", err);
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