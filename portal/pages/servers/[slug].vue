<template>
  <div
    v-if="isLoading"
    class="d-flex justify-center align-center"
    style="height: 400px"
  >
    <v-progress-circular indeterminate color="primary" />
  </div>
  <div v-else-if="server">
    <v-row>
      <!-- Left Column (Image, Info, Owner Info) -->
      <v-col cols="12" md="4" class="d-flex flex-column">
        <div class="d-flex align-center mb-4">
          <!-- Back Button -->
          <v-btn icon class="mr-2 mt-1" size="small" @click="navigateBack">
            <v-icon>mdi-arrow-left</v-icon>
          </v-btn>

          <!-- Server Logo -->
          <v-hover v-slot="{ isHovering, props: hoverProps }">
            <v-img
              v-bind="hoverProps"
              :src="server.imageUrl || '/images/default-server.svg'"
              :alt="`${server.name} logo`"
              height="100"
              width="200"
              cover
              class="rounded-lg server-logo"
            >
              <!-- Optional: Overlay for upload button on hover -->
              <v-fade-transition>
                <div
                  v-if="isHovering && canManageServer"
                  class="d-flex align-center justify-center fill-height bg-black bg-opacity-50"
                >
                  <v-btn
                    icon="mdi-upload"
                    variant="plain"
                    @click.stop="showUploadLogoDialog = true"
                  />
                </div>
              </v-fade-transition>
            </v-img>
          </v-hover>
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

      <!-- Right Column (Details, Tools/Skills/Endpoints, Actions) -->
      <v-col cols="12" md="8">
        <div class="d-flex align-center mb-4 flex-wrap">
          <h1 class="text-h3 mr-2">{{ server.name }}</h1>
          <v-chip v-if="server.protocol" color="info" size="small">{{
            server.protocol
          }}</v-chip>
          <v-spacer />

          <!-- Action Buttons -->
          <div class="d-flex align-center mt-2 mt-sm-0">
            <!-- Configure Headers Button for Subscribers -->
            <v-btn
              v-if="isSubscribedAndHasTemplate"
              color="secondary"
              class="mx-1"
              size="small"
              prepend-icon="mdi-tune"
              @click="showSubscriptionHeadersDialog = true"
            >
              Configure Headers
            </v-btn>

            <!-- Server management buttons (Owner/Admin) -->
            <template v-if="canManageServer">
              <v-btn
                color="primary"
                class="mx-1"
                size="small"
                data-testid="upload-logo-button"
                prepend-icon="mdi-upload"
                @click="openUploadDialog"
              >
                Upload Logo
              </v-btn>
              <v-btn
                color="primary"
                class="mx-1"
                size="small"
                prepend-icon="mdi-pencil"
                @click="openEditDialog"
              >
                Edit
              </v-btn>
              <v-btn
                color="error"
                class="mx-1"
                size="small"
                prepend-icon="mdi-delete"
                @click="openDeleteDialog"
              >
                Delete
              </v-btn>
            </template>
          </div>
        </div>

        <p class="text-body-1 mb-6">{{ server.description }}</p>

        <!-- Display components based on server protocol -->
        <MCPServerTools
          v-if="server.protocol === 'MCP'"
          :tools="server.tools"
        />
        <A2AServerSkills
          v-else-if="server.protocol === 'A2A'"
          :skills="server.a2aSkills"
        />
        <RESTServerFuncs
          v-else-if="server.protocol === 'REST'"
          :endpoints="server.restEndpoints"
        />
      </v-col>
    </v-row>

    <!-- Dialogs -->
    <DeleteServerDialog v-model="deleteDialog" @confirm="deleteServer" />

    <UploadServerLogoDialog
      v-if="showUploadLogoDialog"
      v-model="showUploadLogoDialog"
      :server-slug="serverSlug"
      @logo-updated="handleLogoUpdate"
    />

    <SubscriptionHeaderValuesDialog
      v-if="showSubscriptionHeadersDialog && server"
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
import { ref, computed, onMounted } from "vue";
import { useRoute, useRouter } from "vue-router"; // Import useRouter
import ServerInfo from "~/components/ServerInfo.vue";
import ServerInfoForOwners from "~/components/ServerInfoForOwners.vue";
import DeleteServerDialog from "~/components/DeleteServerDialog.vue";
import MCPServerTools from "~/components/MCPServerTools.vue";
import A2AServerSkills from "~/components/A2AServerSkills.vue";
import RESTServerFuncs from "~/components/RESTServerFuncs.vue";
import UploadServerLogoDialog from "~/components/UploadServerLogoDialog.vue";
import SubscriptionHeaderValuesDialog from "~/components/SubscriptionHeaderValuesDialog.vue";
import type { Server } from "~/utils/server";
import { useSnackbar } from "~/composables/useSnackbar";

interface OwnerUser {
  id: string;
  name: string | null;
  email: string;
}

interface ToolParameter {
  id: string;
  name: string;
  type: string;
  description: string | null;
  required: boolean;
}

const { $auth, $api } = useNuxtApp();
const route = useRoute();
const router = useRouter(); // Use router for navigation
const { showError, showSuccess } = useSnackbar();

const serverSlug = route.params.slug as string;
const isLoading = ref(true);
const deleteDialog = ref(false);
const showSubscriptionHeadersDialog = ref(false);
const showUploadLogoDialog = ref(false); // State for the logo upload dialog

const isAuthenticated = computed(() => $auth.check());
const isSecurityOrAdmin = computed(() => $auth.isSecurityOrAdmin());

const server = ref<Server | null>(null);

// Recalculate permissions based on fetched server data
const canManageServer = computed(() => {
  if (!isAuthenticated.value || !server.value) return false;
  if (isSecurityOrAdmin.value) return true;
  const currentUser = $auth.getUser();
  if (!currentUser) return false;
  // Ensure server.owners is an array before using .some()
  return (
    Array.isArray(server.value.owners) &&
    server.value.owners.some((owner) => owner.user?.id === currentUser.id)
  );
});

// Check if user is subscribed AND the server has a template
const isSubscribedAndHasTemplate = computed(() => {
  return (
    server.value?.isCurrentUserSubscribed &&
    (server.value as any).subscriptionHeaderTemplate && // Check if template exists (fetch adds this)
    (server.value as any).subscriptionHeaderTemplate.length > 0
  );
});

onMounted(async () => {
  await fetchServer();
});

async function fetchServer() {
  isLoading.value = true;
  server.value = null; // Reset server data before fetch

  try {
    // Fetch server using the SLUG, include template info
    // Adjust the type to expect subscriptionHeaderTemplate potentially
    // The API `/servers/[slug].get.ts` already includes owners, _count etc.
    // and calculates `isCurrentUserSubscribed`, `isCurrentUserOwner`, `subscriptionId`
    server.value = await $api.getJson<
      Server & { subscriptionHeaderTemplate?: any[] }
    >(`/servers/${serverSlug}`);
  } catch (error: unknown) {
    console.error("Error fetching server details:", error);
    const message =
      error instanceof Error ? error.message : "Failed to load server details.";
    showError(message);
    // Optionally redirect if server not found (e.g., error status 404)
    // if (error?.response?.status === 404) {
    //    router.push('/servers');
    // }
  } finally {
    isLoading.value = false;
  }
}

function navigateBack() {
  router.push("/servers"); // Navigate back to the catalog
}

function openEditDialog() {
  router.push(`/servers/edit/${serverSlug}`);
}

function openUploadDialog() {
  console.log("openDeleteDialog");
  showUploadLogoDialog.value = true;
}

function openDeleteDialog() {
  deleteDialog.value = true;
}

async function deleteServer() {
  if (!server.value) return;
  try {
    await $api.deleteJson(`/servers/${serverSlug}`);
    showSuccess("Server deleted successfully.");
    router.push("/servers"); // Navigate back to catalog after delete
  } catch (error: unknown) {
    const message =
      error instanceof Error ? error.message : "Failed to delete server.";
    showError(message);
    console.error("Error deleting server:", error);
  } finally {
    deleteDialog.value = false;
  }
}

// Handle subscription update from child (ServerInfo)
function handleSubscriptionUpdate() {
  // Re-fetch server data to get updated subscription status and potentially subscriptionId
  // This will also update the `isCurrentUserSubscribed` flag used by `isSubscribedAndHasTemplate`
  fetchServer();
}

// Handle header save from SubscriptionHeaderValuesDialog
function onSubscriptionHeadersSaved() {
  // Optionally refetch data if needed, but dialog already saved.
  console.log("Subscription headers saved.");
  showSuccess("Subscription headers updated!"); // Show feedback
}

// Handler for when the logo is successfully updated via UploadServerLogoDialog
function handleLogoUpdate(newImageUrl: string) {
  if (server.value) {
    server.value.imageUrl = newImageUrl; // Update local data to refresh image instantly
  }
  // No need to close dialog here, component does it
}

useHead({
  title: computed(() => server.value?.name || "Server Details"),
});
</script>

<style scoped>
/* Optional: Add some styling for the logo hover effect */
.server-logo {
  transition: filter 0.3s ease;
  position: relative; /* Needed for absolute positioning of overlay */
}
/* Example hover effect - could be removed if button overlay is enough */
/* .server-logo:hover {
  filter: brightness(0.8);
} */
.bg-opacity-50 {
  background-color: rgba(0, 0, 0, 0.5);
}
</style>
