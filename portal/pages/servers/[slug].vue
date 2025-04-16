<template>
  <div v-if="isLoading" class="d-flex justify-center align-center" style="height: 400px;">
    <v-progress-circular indeterminate color="primary"/>
  </div>
  <div v-else-if="server">
    <v-row>
      <v-col cols="12" md="4">
        <v-btn icon class="mr-2" @click="navigateTo('/servers')">
            <v-icon>mdi-arrow-left</v-icon>
          </v-btn>

        <v-img
          :src="server.imageUrl || '/images/default-server.svg'"
          height="100"
          width="200"
          cover
          class="rounded-lg"
        />

        <ServerInfo
          :server="server"
          :is-authenticated="isAuthenticated"
          class="mt-4"
        />

        <ServerInfoForOwners
          :server="server"
          :is-authenticated="isAuthenticated"
          class="mt-4"
        />
      </v-col>

      <v-col cols="12" md="8">
        <div class="d-flex align-center mb-4">
           <h1 class="text-h3 mr-2">{{ server.name }}</h1>
           <v-chip v-if="server.protocol" color="info" size="small">{{ server.protocol }}</v-chip>
          <v-spacer />

          <!-- Server management buttons for owners, admins and security -->
          <div v-if="canManageServer" class="d-flex">
            <v-btn
              color="primary"
              class="mx-1"
              @click="openEditDialog"
            >
              <v-icon left>mdi-pencil</v-icon>
              Edit
            </v-btn>
            <v-btn
              color="error"
              class="mx-1"
              @click="openDeleteDialog"
            >
              <v-icon left>mdi-delete</v-icon>
              Delete
            </v-btn>
          </div>
        </div>

        <p class="text-body-1 mb-6">{{ server.description }}</p>

        <!-- Display components based on server protocol -->
        <MCPServerTools v-if="server.protocol === 'MCP'" :tools="server.tools" :is-authenticated="isAuthenticated" />
        <A2AServerSkills v-else-if="server.protocol === 'A2A'" :skills="server.a2aSkills" :is-authenticated="isAuthenticated" />
        <RESTServerFuncs v-else-if="server.protocol === 'REST'" :endpoints="server.restEndpoints" :is-authenticated="isAuthenticated" />
      </v-col>
    </v-row>

    <!-- Delete Confirmation Dialog -->
    <DeleteServerDialog
      v-model="deleteDialog"
      @confirm="deleteServer"
    />
  </div>
   <div v-else class="text-center py-10">
      <v-alert type="error" variant="tonal">
         Server not found or you do not have permission to view it.
      </v-alert>
       <v-btn color="primary" to="/servers" class="mt-4">Back to Catalog</v-btn>
   </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { useRoute } from 'vue-router'; 
import ServerInfo from '~/components/ServerInfo.vue';
import ServerInfoForOwners from '~/components/ServerInfoForOwners.vue';
import DeleteServerDialog from '~/components/DeleteServerDialog.vue';
import MCPServerTools from '~/components/MCPServerTools.vue';
import A2AServerSkills from '~/components/A2AServerSkills.vue';
import RESTServerFuncs from '~/components/RESTServerFuncs.vue';
import type { Server } from '~/utils/server'; // Use the updated Server type
import { useSnackbar } from '~/composables/useSnackbar'; // Import useSnackbar

const { $auth, $api } = useNuxtApp();
const route = useRoute();
const { showError, showSuccess } = useSnackbar(); // Use snackbar for errors and success

const serverSlug = route.params.slug as string; // Get slug from route
const isLoading = ref(true);
const deleteDialog = ref(false);

// Authentication
const isAuthenticated = computed(() => $auth.check());
const isSecurityOrAdmin = computed(() => $auth.isSecurityOrAdmin());

// Check if user can manage the server (owner, admin, security)
const canManageServer = computed(() => {
  if (!isAuthenticated.value || !server.value) return false;
  if (isSecurityOrAdmin.value) return true;

  // Check if current user is an owner
  const currentUser = $auth.getUser();
  if (!currentUser) return false;

  // Use optional chaining and check user.id
  return server.value.owners?.some(owner => owner.user?.id === currentUser.id) || false;
});

// Server data
const server = ref<Server | null>(null);

onMounted(async () => {
  await fetchServer();
});

async function fetchServer() {
  isLoading.value = true;
  server.value = null; // Reset server data before fetching

  try {
    // Fetch server using the SLUG
    server.value = await $api.getJson<Server>(`/servers/${serverSlug}`);
  } catch (error: unknown) {
    console.error('Error fetching server details:', error);
    const message = error instanceof Error ? error.message : 'Failed to load server details.';
    showError(message);
    // Don't navigate away immediately, let the template show the error message
    // if (error instanceof Error && error.message.includes('404')) {
    //   // Server not found
    //   navigateTo('/servers');
    // }
  } finally {
    isLoading.value = false;
  }
}

// Open the edit server page using SLUG
function openEditDialog() {
  navigateTo(`/servers/edit/${serverSlug}`); // Use slug
}

// Open the delete confirmation dialog
function openDeleteDialog() {
  deleteDialog.value = true;
}

// Delete the server using SLUG
async function deleteServer() {
  if (!server.value) return; // Should not happen if button is visible

  try {
    await $api.deleteJson(`/servers/${serverSlug}`); // Use slug
    showSuccess('Server deleted successfully.');
    navigateTo('/servers'); // Navigate back to servers list
  } catch (error: unknown) {
    const message = error instanceof Error ? error.message : 'Failed to delete server.';
    showError(message);
    console.error('Error deleting server:', error);
  } finally {
    deleteDialog.value = false;
  }
}

// Update Nuxt page meta dynamically
useHead({
  title: computed(() => server.value?.name || 'Server Details')
})
</script>