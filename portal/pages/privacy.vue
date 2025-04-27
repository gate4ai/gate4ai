<template>
  <v-container>
    <v-row justify="center">
      <v-col cols="12" md="10" lg="8">
        <v-card class="pa-6">
          <v-card-title class="text-h4 mb-4">Privacy Policy</v-card-title>

          <v-card-text>
            <h2 class="text-h5 mb-3">Data Collection</h2>
            <p class="mb-4">
              We collect only the minimum necessary data (email, API call logs,
              technical metadata). Anonymous metrics are collected to analyze
              service usage. Passwords are not stored – we use OAuth or hashing
              instead.
            </p>

            <h2 class="text-h5 mb-3">Use of Data</h2>
            <p class="mb-4">
              Data is used only for technical support and service analysis. We
              do not sell user data to third parties.
            </p>

            <h2 class="text-h5 mb-3">Data Storage</h2>
            <p class="mb-4">
              Logs are stored only for technical necessity. Users can request
              the deletion of their data.
            </p>

            <h2 class="text-h5 mb-3">Security</h2>
            <p class="mb-4">
              Standard security measures are in place, but 100% security is not
              guaranteed.
            </p>

            <h2 class="text-h5 mb-3">Sharing with Third Parties</h2>
            <p class="mb-4">
              Personal data is not shared with third parties, except as required
              by law. API requests may pass through external API-servers, for
              whose security we are not responsible.
            </p>

            <h2 class="text-h5 mb-3">Changes to the Privacy Policy</h2>
            <p class="mb-4">
              The policy may change without notice. The user is responsible for
              reviewing updates.
            </p>
          </v-card-text>

          <v-card-actions class="mt-4">
            <v-btn color="primary" to="/" variant="outlined"
              >Return to Home</v-btn
            >
          </v-card-actions>
        </v-card>
      </v-col>
    </v-row>
  </v-container>
</template>

<script setup lang="ts">
definePageMeta({
  title: "Privacy Policy",
  layout: "default",
});
</script>
