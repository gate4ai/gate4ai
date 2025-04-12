<template>
  <div>
    <v-container>
      <div class="d-flex align-center mb-6">
        <v-btn icon class="mr-2" @click="navigateBack">
          <v-icon>mdi-arrow-left</v-icon>
        </v-btn>
        <h1 class="text-h3">Edit Server</h1>
      </div>

       <!-- Loading State -->
       <div v-if="isLoading" class="d-flex justify-center align-center my-8">
         <v-progress-circular indeterminate color="primary" />
       </div>

      <!-- Server Form -->
      <v-card v-else-if="serverDataForForm" class="pa-4">
        <ServerForm
          :server-data="serverDataForForm"
          :is-submitting="isSubmitting"
          submit-label="Update Server"
          @submit="updateServer"
          @cancel="navigateBack"
        />
      </v-card>

      <!-- Error/Not Found State -->
       <div v-else class="text-center py-10">
         <v-alert type="error" variant="tonal">
           {{ loadError || 'Server not found or could not be loaded.' }}
         </v-alert>
          <v-btn color="primary" :to="`/servers/${serverSlug}`" class="mt-4">Back to Server Details</v-btn>
       </div>

    </v-container>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue';
import { useRoute, useRouter } from 'vue-router'; // Import useRouter
import ServerForm from '~/components/ServerForm.vue';
// Use the specific type for the form
import type { ServerData } from '~/utils/server';
import { useSnackbar } from '~/composables/useSnackbar';
import type { ServerStatus, ServerAvailability, ServerType } from '@prisma/client';

// Define the shape of data expected from the API GET call
// This might be slightly different from ServerData if API returns more fields
interface ServerApiResponse {
  id: string;
  name: string;
  slug: string;
  type: ServerType;
  description?: string | null;
  website?: string | null;
  email?: string | null;
  imageUrl?: string | null;
  serverUrl: string;
  status: ServerStatus;
  availability: ServerAvailability;
  // ... other fields if API returns more
}

// Define the shape of the data passed to the ServerForm component
// This should match the `ServerFormData` interface in ServerForm.vue
interface ServerFormData extends ServerData {
  id: string; // ID is required for editing
  slug: string;
  type: ServerType;
  serverUrl: string;
  status: ServerStatus;
  availability: ServerAvailability;
}


const { $auth, $api } = useNuxtApp();
const route = useRoute();
const router = useRouter(); // Use useRouter for navigation
const serverSlug = route.params.slug as string; // Get slug
const isLoading = ref(true);
const isSubmitting = ref(false);
const { showError, showSuccess } = useSnackbar();
const loadError = ref<string | null>(null);

// Use the specific ServerFormData type for the reactive ref
const serverDataForForm = ref<ServerFormData | null>(null);

// Check if user is authenticated
const isAuthenticated = computed(() => $auth.check());

onMounted(async () => {
  // Check authentication first
  if (!isAuthenticated.value) {
    navigateTo(`/login?redirect=${route.fullPath}`);
    return;
  }

  // Fetch server details
  await fetchServer();
});

async function fetchServer() {
  isLoading.value = true;
  loadError.value = null;
  serverDataForForm.value = null; // Reset before fetch

  try {
    // Fetch using slug
    const data = await $api.getJson<ServerApiResponse>(`/servers/${serverSlug}`);

    // Transform API response to the structure needed by ServerForm
    serverDataForForm.value = {
      id: data.id,
      name: data.name,
      slug: data.slug,
      type: data.type,
      description: data.description || '',
      website: data.website || '',
      email: data.email || '',
      imageUrl: data.imageUrl || '',
      serverUrl: data.serverUrl, // Ensure these fields exist in ServerApiResponse
      status: data.status,
      availability: data.availability,
    };

  } catch (err: unknown) {
     console.error('Error fetching server details for edit:', err);
     const message = err instanceof Error ? err.message : 'Failed to load server details for editing.';
     loadError.value = message; // Store error message for display
     showError(message); // Show snackbar

    // Redirect only if it's specifically a permission error (e.g., 403 from backend check)
    if (err instanceof Error && 'statusCode' in err && (err as any).statusCode === 403) {
         navigateTo(`/servers/${serverSlug}`); // Redirect back to view page
    }
    // Keep the user on the edit page for other errors (like 404, 500) so they see the error message
  } finally {
    isLoading.value = false;
  }
}

// Update server using SLUG in the URL
async function updateServer(updatedServerData: ServerFormData) {
  if (!updatedServerData) return;

  isSubmitting.value = true;

  try {
    // Prepare payload - only send fields allowed by the API endpoint's validation schema
    const payload = {
      name: updatedServerData.name,
      //slug: updatedServerData.slug, //  The 'slug' should not be updated via this endpoint.
      type: updatedServerData.type, // Send type for update
      description: updatedServerData.description || null,
      website: updatedServerData.website || null,
      email: updatedServerData.email || null,
      imageUrl: updatedServerData.imageUrl || null,
      serverUrl: updatedServerData.serverUrl,
      status: updatedServerData.status,
      availability: updatedServerData.availability
    };

    // Call PUT endpoint using the slug
    await $api.putJson(`/servers/${serverSlug}`, payload);

    showSuccess('Server updated successfully');
    navigateTo(`/servers/${serverSlug}`); // Navigate back to server details page using slug

  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : 'Failed to update server.';
    showError(message);
    console.error("Error updating server:", err);
  } finally {
    isSubmitting.value = false;
  }
}

// Navigate back using SLUG
function navigateBack() {
  navigateTo(`/servers/${serverSlug}`); // Use slug
}

// Update Nuxt page meta
useHead({
  title: computed(() => `Edit ${serverDataForForm.value?.name || 'Server'}`)
})
</script>