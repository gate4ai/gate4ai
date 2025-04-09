// /home/alex/go-ai/gate4ai/www/app.vue
<template>
  <v-app>
    <v-app-bar app color="primary" dark>
      <v-app-bar-title>
        <img src="/images/logo.svg" alt="gate4.ai" >
        <router-link to="/" class="text-decoration-none text-white">
          GATE4.AI
        </router-link>
      </v-app-bar-title>
      <v-spacer/>
      <v-btn to="/servers" text>Servers</v-btn>

      <!-- Wrap auth-dependent buttons in ClientOnly -->
      <ClientOnly>
        <!-- Default slot: Renders only on the client -->
        <template #default>
          <v-btn v-if="isAuthenticated && isSecurityOrAdmin" to="/users" text>Users</v-btn>
          <v-btn v-if="isAuthenticated && isAdmin" to="/settings" text>Settings</v-btn>
          <v-btn v-if="!isAuthenticated" to="/login" text>Login</v-btn>
          <v-btn v-if="!isAuthenticated" to="/register" text>Register</v-btn>

          <v-menu v-if="isAuthenticated">
            <template #activator="{ props }">
              <v-btn icon v-bind="props">
                <v-icon>mdi-account-circle</v-icon>
              </v-btn>
            </template>
            <v-list>
              <v-list-item to="/servers?filter=subscribed">
                <v-list-item-title>Subscribed Servers</v-list-item-title>
              </v-list-item>
              <v-list-item to="/servers?filter=owned">
                <v-list-item-title>Published Servers</v-list-item-title>
              </v-list-item>
              <v-list-item to="/profile">
                <v-list-item-title>Profile</v-list-item-title>
              </v-list-item>
              <v-list-item to="/keys">
                <v-list-item-title>My API Keys</v-list-item-title>
              </v-list-item>
              <v-list-item @click="logout">
                <v-list-item-title>Logout</v-list-item-title>
              </v-list-item>
            </v-list>
          </v-menu>
        </template>

        <!-- Fallback slot: Renders on SSR and on client before hydration/mounting -->
        <template #fallback>
           <!-- Show simple placeholders or nothing -->
           <v-skeleton-loader type="button@2" class="d-inline-flex ml-2" />
           <!-- Optional: Add a placeholder for the avatar if needed -->
           <!-- <v-skeleton-loader type="avatar" class="d-inline-flex ml-2" /> -->
        </template>
      </ClientOnly>
    </v-app-bar>

    <v-main>
      <v-container fluid>
        <NuxtPage />
      </v-container>
    </v-main>

    <v-snackbar
      v-model="snackbar.show"
      :color="snackbar.color"
      :timeout="snackbar.timeout"
      location="top right"
       multi-line
    >
      {{ snackbar.text }}
      <template #actions>
        <v-btn
          icon="mdi-close"
          variant="text"
          @click="hideSnackbar"
        />
      </template>
    </v-snackbar>

    <v-footer app color="primary" dark>
      <v-row justify="center" no-gutters>
        <v-col class="text-center" cols="12">
          {{ new Date().getFullYear() }} — <strong>gate4.ai</strong>
        </v-col>
      </v-row>
    </v-footer>
  </v-app>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { useSnackbar } from '~/composables/useSnackbar';

const { snackbar, hideSnackbar } = useSnackbar();
const { $auth } = useNuxtApp();

const isAuthenticated = computed(() => $auth.check());
const isSecurityOrAdmin = computed(() => $auth.isSecurityOrAdmin());
const isAdmin = computed(() => $auth.isAdmin());

function logout() {
  $auth.logout();
}
</script>

<style>
body {
  margin: 0;
  font-family: 'Open Sans', sans-serif;
}
/* Optional: Style skeleton loaders if used */
.v-skeleton-loader {
  background-color: rgba(255, 255, 255, 0.1); /* Adjust color for dark theme */
}
</style>