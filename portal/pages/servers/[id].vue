<template>
  <div v-if="server">
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
          <h1 class="text-h3">{{ server.name }}</h1>
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
        
        <ServerTools :tools="server.tools" :is-authenticated="isAuthenticated" />
      </v-col>
    </v-row>

    <!-- Delete Confirmation Dialog -->
    <DeleteServerDialog 
      v-model="deleteDialog"
      @confirm="deleteServer"
    />
  </div>
  <div v-else class="d-flex justify-center align-center" style="height: 400px;">
    <v-progress-circular indeterminate color="primary"/>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import ServerInfo from '~/components/ServerInfo.vue';
import ServerInfoForOwners from '~/components/ServerInfoForOwners.vue';
import DeleteServerDialog from '~/components/DeleteServerDialog.vue';
import ServerTools from '~/components/ServerTools.vue';
import type { Server } from '~/utils/server';

const { $auth, $api } = useNuxtApp();
const route = useRoute();
const serverId = route.params.id as string;
const isLoading = ref(true);
const deleteDialog = ref(false);

// Authentication
const isAuthenticated = computed(() => $auth.check());
const isSecurityOrAdmin = computed(() => $auth.isSecurityOrAdmin());

// Check if user can manage the server (owner, admin, security)
const canManageServer = computed(() => {
  if (!isAuthenticated.value) return false;
  if (isSecurityOrAdmin.value) return true;
  if (!server.value) return false;
  
  // Check if current user is an owner
  const currentUser = $auth.getUser();
  if (!currentUser) return false;
  
  return server.value.owners?.some((owner: { user: { id: string } }) => owner.user.id === currentUser.id) || false;
});

// Server data
const server = ref<Server | null>(null);

onMounted(async () => {
  await fetchServer();
});

async function fetchServer() {
  isLoading.value = true;
  
  try {
    server.value = await $api.getJson(`/servers/${serverId}`);
  } catch (error: unknown) {
    console.error('Error fetching server details:', error);
    if (error instanceof Error && error.message.includes('404')) {
      // Server not found
      navigateTo('/servers');
      return;
    }
    // Show error message to user
  } finally {
    isLoading.value = false;
  }
}

// Open the edit server page
function openEditDialog() {
  navigateTo(`/servers/edit/${serverId}`);
}

// Open the delete confirmation dialog
function openDeleteDialog() {
  deleteDialog.value = true;
}

// Delete the server
async function deleteServer() {
  try {
    await $api.deleteJson(`/servers/${serverId}`);
    
    // Navigate back to servers list
    navigateTo('/servers');
  } catch (error: unknown) {
    console.error('Error deleting server:', error);
    // Show error message to user
  } finally {
    deleteDialog.value = false;
  }
}
</script>