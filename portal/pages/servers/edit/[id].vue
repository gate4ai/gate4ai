<template>
  <div>
    <v-container>
      <div class="d-flex align-center mb-6">
        <v-btn icon class="mr-2" @click="navigateBack">
          <v-icon>mdi-arrow-left</v-icon>
        </v-btn>
        <h1 class="text-h3">Edit Server</h1>
      </div>

      <v-card v-if="server" class="pa-4">
        <ServerForm 
          :server-data="server"
          :is-submitting="isSubmitting"
          submit-label="Update Server"
          @submit="updateServer"
          @cancel="navigateBack"
        />
      </v-card>

      <div v-else-if="isLoading" class="d-flex justify-center align-center my-8">
        <v-progress-circular indeterminate color="primary" />
      </div>
    </v-container>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue';
import ServerForm from '~/components/ServerForm.vue';
import type { ServerData } from '~/utils/server';
import { useSnackbar } from '~/composables/useSnackbar';

const { $auth } = useNuxtApp();
const route = useRoute();
const serverId = route.params.id as string;
const isLoading = ref(true);
const isSubmitting = ref(false);
const { showError, showSuccess } = useSnackbar();

// Server data
interface Server {
  id: string;
  name: string;
  description: string;
  website?: string;
  email?: string;
  imageUrl?: string;
  serverUrl: string;
  status: string;
  availability: string;
}

// Use the same interface as defined in ServerForm
interface ServerFormData extends ServerData {
  id?: string;
  serverUrl: string;
  status: string;
  availability: string;
}

const server = ref<Server | null>(null);

// Check if user is authenticated
const isAuthenticated = computed(() => $auth.check());

onMounted(async () => {
  // Check permissions first
  if (!isAuthenticated.value) {
    navigateTo('/login');
    return;
  }
  
  // Fetch server details
  await fetchServer();
});

async function fetchServer() {
  isLoading.value = true;
  
  try {
    const { $api } = useNuxtApp();
    
    try {
      const data = await $api.getJson(`/servers/${serverId}`);
      server.value = {
        id: data.id,
        name: data.name,
        description: data.description || '',
        website: data.website || '',
        email: data.email || '',
        imageUrl: data.imageUrl || '',
        serverUrl: data.serverUrl || '',
        status: data.status || 'DRAFT',
        availability: data.availability || 'PRIVATE'
      };
    } catch (apiError: unknown) {
      if (apiError instanceof Error) {
        if (apiError.message.includes('404')) {
          navigateTo('/servers');
          return;
        }
        
        if (apiError.message.includes('403')) {
          showError('You do not have permission to edit this server');
          navigateTo(`/servers/${serverId}`);
          return;
        }
      }
      throw apiError;
    }
  } catch (err: unknown) {
    console.error('Error fetching server details:', err);
    showError(err instanceof Error ? err.message : 'Failed to load server details');
  } finally {
    isLoading.value = false;
  }
}

async function updateServer(updatedServer: ServerFormData) {
  if (!updatedServer) return;
  
  isSubmitting.value = true;
  
  try {
    const { $api } = useNuxtApp();
    
    await $api.putJson(`/servers/${serverId}`, {
      name: updatedServer.name,
      description: updatedServer.description,
      website: updatedServer.website || null,
      email: updatedServer.email || null,
      imageUrl: updatedServer.imageUrl || null,
      serverUrl: updatedServer.serverUrl,
      status: updatedServer.status,
      availability: updatedServer.availability
    });
    
    // Navigate back to server details page
    navigateTo(`/servers/${serverId}`);
    showSuccess('Server updated successfully');
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('Failed to update server');
    }
    console.error("Error updating server:", err);
  } finally {
    isSubmitting.value = false;
  }
}

function navigateBack() {
  navigateTo(`/servers/${serverId}`);
}
</script> 