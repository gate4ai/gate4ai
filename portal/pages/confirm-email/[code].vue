<template>
  <v-container fill-height>
    <v-row align="center" justify="center">
      <v-col cols="12" sm="8" md="6" lg="4">
        <v-card class="pa-4 text-center">
          <v-card-title class="text-h5 mb-4">Email Confirmation</v-card-title>
          <v-card-text>
            <p>Processing your email confirmation...</p>
            <!-- Optional: Add a progress indicator -->
            <v-progress-circular indeterminate color="primary" class="my-4" />
            <p v-if="message">{{ message }}</p>
          </v-card-text>
           <v-card-actions class="justify-center">
             <v-btn color="primary" to="/login">Go to Login</v-btn>
           </v-card-actions>
        </v-card>
      </v-col>
    </v-row>
  </v-container>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';

definePageMeta({
  title: 'Confirming Email',
  layout: 'public', // Use a layout without login requirements if you have one
});

const route = useRoute();
const router = useRouter();
const message = ref('');

onMounted(() => {
  // The backend API handles the actual confirmation and redirects.
  // This page primarily serves as a landing spot during the brief
  // moment the backend processes the GET request.
  // We can add a fallback redirect in case something goes wrong client-side.
  message.value = 'Redirecting you shortly...';
  setTimeout(() => {
    // Fallback redirect if backend redirect fails
    if (route.path.startsWith('/confirm-email')) { // Check if still on this page
        router.push('/login?confirmed=fallback');
    }
  }, 3000); // Redirect after 3 seconds if still here
});
</script>

<style scoped>
.v-container {
  min-height: 80vh;
}
</style> 