<template>
  <v-row justify="center" align="center" class="auth-container">
    <v-col cols="12" sm="8" md="6" lg="4">
      <v-card class="pa-4">
        <v-card-title class="text-center text-h4 mb-4">
          Create an Account
        </v-card-title>
        
        <v-form ref="form" @submit.prevent="handleRegister">
          <v-card-text>
            <v-text-field
              v-model="name"
              label="Full Name"
              required
              :rules="[rules.required]"
              variant="outlined"
              prepend-inner-icon="mdi-account"
              class="mb-4"
            />
            
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
              :rules="[rules.required, rules.password]"
              variant="outlined"
              prepend-inner-icon="mdi-lock"
              :append-inner-icon="showPassword ? 'mdi-eye-off' : 'mdi-eye'"
              class="mb-4"
              @click:append-inner="showPassword = !showPassword"
            />
            
            <v-text-field
              v-model="confirmPassword"
              label="Confirm Password"
              :type="showPassword ? 'text' : 'password'"
              required
              :rules="[rules.required, v => v === password || 'Passwords do not match']"
              variant="outlined"
              prepend-inner-icon="mdi-lock-check"
              :append-inner-icon="showPassword ? 'mdi-eye-off' : 'mdi-eye'"
              class="mb-4"
              @click:append-inner="showPassword = !showPassword"
            />
            
            <v-checkbox
              v-model="agreeTerms"
              :rules="[rules.agree]"
              label="I agree to the"
              class="mb-4"
            >
              <template #label>
                <span style="white-space: nowrap;">I agree to the 
                <router-link to="/terms" class="text-decoration-none">Terms of Service</router-link>
                and 
                <router-link to="/privacy" class="text-decoration-none">Privacy Policy</router-link></span>
              </template>
            </v-checkbox>
            
            <v-btn
              type="submit"
              color="primary"
              block
              size="large"
              :loading="isLoading"
              class="mb-4"
            >
              Register
            </v-btn>
            
            <div class="text-center">
              <span class="text-medium-emphasis">Already have an account?</span>
              <v-btn
                variant="text"
                color="primary"
                to="/login"
                class="ml-2"
              >
                Login
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
import type { User } from '@prisma/client';

definePageMeta({
  title: 'Register',
  layout: 'default',
});

const { $api, $auth } = useNuxtApp();
const { showError } = useSnackbar();
const route = useRoute();
const redirectPath = route.query.redirect as string || '/servers';

const name = ref('');
const email = ref('');
const password = ref('');
const confirmPassword = ref('');
const showPassword = ref(false);
const agreeTerms = ref(false);
const isLoading = ref(false);
const form = ref<{ validate: () => Promise<{ valid: boolean }> } | null>(null);

interface RegisterResponse {
    token: string;
    user: User;
    message?: string;
}

async function handleRegister() {
  if (!form.value) return;

  const { valid } = await form.value.validate();
  if (!valid) {
    console.log("Validation failed");
    return;
  }

  isLoading.value = true;

  try {
    const data = await $api.postJson<RegisterResponse>('auth/register', {
      name: name.value,
      email: email.value,
      password: password.value
    });

    if (!data || !data.token || !data.user) {
      throw new Error(data?.message || 'Registration failed: Invalid response from server');
    }
    $auth.login(data.token, data.user);

    navigateTo(redirectPath);

  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('An error occurred during registration. Please try again.');
    }
    console.error("Registration error:", err);
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