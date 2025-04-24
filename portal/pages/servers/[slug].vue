<template>
  <div v-if="isLoading" class="d-flex justify-center align-center" style="height: 400px;">
    <v-progress-circular indeterminate color="primary"/>
  </div>
  <div v-else-if="server">
    <v-row>
      <!-- Left Column (Image, Info, Owner Info) -->
      <v-col cols="12" md="4">
         <div class="d-flex align-start mb-4">
           <v-btn icon class="mr-2 mt-1" @click="navigateTo('/servers')" size="small">
             <v-icon>mdi-arrow-left</v-icon>
           </v-btn>
           <v-img
             :src="server.imageUrl || '/images/default-server.svg'"
             height="100"
             width="200"
             cover
             class="rounded-lg"
           />
         </div>

        <ServerInfo
          :server="server"
          :is-authenticated="isAuthenticated"
          class="mt-4"
          @subscribe="handleSubscriptionUpdate"
        />

        <ServerInfoForOwners
          :server="server"
          :is-authenticated="isAuthenticated"
          class="mt-4"
        />
      </v-col>

      <!-- Right Column (Details, Tools/Skills/Endpoints) -->
      <v-col cols="12" md="8">
        <div class="d-flex align-center mb-4">
           <h1 class="text-h3 mr-2">{{ server.name }}</h1>
           <v-chip v-if="server.protocol" color="info" size="small">{{ server.protocol }}</v-chip>
          <v-spacer />

           <!-- NEW: Configure Headers Button for Subscribers -->
           <v-btn
             v-if="isSubscribedAndHasTemplate"
             color="secondary"
             class="mx-1"
             @click="showSubscriptionHeadersDialog = true"
           >
             <v-icon left>mdi-tune</v-icon>
             Configure Headers
           </v-btn>

          <!-- Server management buttons -->
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

    <!-- NEW: Subscription Header Values Dialog -->
     <SubscriptionHeaderValuesDialog
       v-if="showSubscriptionHeadersDialog"
       v-model="showSubscriptionHeadersDialog"
       :server-slug="serverSlug"
       :subscription-id="server?.subscriptionId"
       @save="onSubscriptionHeadersSaved"
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
import SubscriptionHeaderValuesDialog from '~/components/SubscriptionHeaderValuesDialog.vue'; // NEW
import type { Server } from '~/utils/server';
import { useSnackbar } from '~/composables/useSnackbar';

const { $auth, $api } = useNuxtApp();
const route = useRoute();
const { showError, showSuccess } = useSnackbar();

const serverSlug = route.params.slug as string;
const isLoading = ref(true);
const deleteDialog = ref(false);
const showSubscriptionHeadersDialog = ref(false); // NEW

const isAuthenticated = computed(() => $auth.check());
const isSecurityOrAdmin = computed(() => $auth.isSecurityOrAdmin());

const server = ref<Server | null>(null);

// Recalculate permissions based on fetched server data
const canManageServer = computed(() => {
  if (!isAuthenticated.value || !server.value) return false;
  if (isSecurityOrAdmin.value) return true;
  const currentUser = $auth.getUser();
  if (!currentUser) return false;
  return server.value.owners?.some(owner => owner.user?.id === currentUser.id) || false;
});

// NEW: Check if user is subscribed AND the server has a template
const isSubscribedAndHasTemplate = computed(() => {
    return server.value?.isCurrentUserSubscribed &&
           (server.value as any).subscriptionHeaderTemplate && // Check if template exists (fetch adds this)
           (server.value as any).subscriptionHeaderTemplate.length > 0;
});


onMounted(async () => {
  await fetchServer();
});

async function fetchServer() {
  isLoading.value = true;
  server.value = null;

  try {
    // Fetch server using the SLUG, include template info
    // Adjust the type to expect subscriptionHeaderTemplate potentially
    server.value = await $api.getJson<Server & { subscriptionHeaderTemplate?: any[] }>(`/servers/${serverSlug}`);
  } catch (error: unknown) {
    console.error('Error fetching server details:', error);
    const message = error instanceof Error ? error.message : 'Failed to load server details.';
    showError(message);
  } finally {
    isLoading.value = false;
  }
}

function openEditDialog() {
  navigateTo(`/servers/edit/${serverSlug}`);
}

function openDeleteDialog() {
  deleteDialog.value = true;
}

async function deleteServer() {
  if (!server.value) return;
  try {
    await $api.deleteJson(`/servers/${serverSlug}`);
    showSuccess('Server deleted successfully.');
    navigateTo('/servers');
  } catch (error: unknown) {
    const message = error instanceof Error ? error.message : 'Failed to delete server.';
    showError(message);
    console.error('Error deleting server:', error);
  } finally {
    deleteDialog.value = false;
  }
}

// NEW: Handle subscription update from child (ServerInfo)
function handleSubscriptionUpdate() {
    // Re-fetch server data to get updated subscription status and potentially subscriptionId
    fetchServer();
}

// NEW: Handle header save from dialog
function onSubscriptionHeadersSaved() {
    // Optionally refetch data if needed, but dialog already saved.
    console.log("Subscription headers saved.");
    // Potentially show a success message again here if desired
}

useHead({
  title: computed(() => server.value?.name || 'Server Details')
})
</script>