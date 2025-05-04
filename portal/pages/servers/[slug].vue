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

      <!-- Right Column (Details, Connection Instructions, Tools/Skills/Endpoints, Actions) -->
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

        <!-- Connection Instructions Section -->
        <ClientOnly>
          <!-- Log props before rendering ServerConnectionInstructions -->
          {{
            logConnectionInstructionProps({
              isAuthenticated,
              requiresSubscription,
              isSubscribedFromComposable:
                subscriptionComposable.subscriptionState.isSubscribed,
              isSubscribedFromServer: server?.isCurrentUserSubscribed ?? false,
              hasImplicitAccess: canManageServer,
            })
          }}
          <ServerConnectionInstructions
            :gateway-base-url="gatewayBaseUrl"
            :server-slug="serverSlug"
            :is-authenticated="isAuthenticated"
            :requires-subscription="requiresSubscription"
            :is-subscribed="
              subscriptionComposable.subscriptionState.isSubscribed
            "
            :has-implicit-access="canManageServer"
            @subscribe-now="triggerSubscriptionFlow"
          />
          <template #fallback>
            <!-- Removed v-skeleton-loader, maybe add simple text -->
            <div class="pa-4 text-center text-grey">
              Loading connection instructions...
            </div>
          </template>
        </ClientOnly>

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

    <!-- Subscription Header Dialog - now uses composable state -->
    <SubscriptionHeaderValuesDialog
      v-if="subscriptionComposable.showHeadersDialog.value"
      v-model="subscriptionComposable.showHeadersDialog.value"
      :server-slug="serverSlug"
      :subscription-id="subscriptionComposable.subscriptionState.subscriptionId"
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
import { ref, computed, onMounted, toRef } from "vue";
import { useRoute, useRouter } from "vue-router"; // Import useRouter
import ServerInfo from "~/components/ServerInfo.vue";
import ServerInfoForOwners from "~/components/ServerInfoForOwners.vue";
import DeleteServerDialog from "~/components/DeleteServerDialog.vue";
import MCPServerTools from "~/components/MCPServerTools.vue";
import A2AServerSkills from "~/components/A2AServerSkills.vue";
import RESTServerFuncs from "~/components/RESTServerFuncs.vue";
import UploadServerLogoDialog from "~/components/UploadServerLogoDialog.vue";
import SubscriptionHeaderValuesDialog from "~/components/SubscriptionHeaderValuesDialog.vue";
import ServerConnectionInstructions from "~/components/ServerConnectionInstructions.vue"; // Import new component
import type { Server } from "~/utils/server";
import { useSnackbar } from "~/composables/useSnackbar";
import { useNuxtApp } from "#app";
import { useSubscription } from "~/composables/useSubscription"; // Import the subscription composable

// Define a type for the template items if needed for better type checking
interface SubscriptionHeaderTemplateItem {
  id: string;
  key: string;
  description?: string | null;
  required: boolean;
}

const { $auth, $api, $settings } = useNuxtApp(); // Add $settings
const route = useRoute();
const router = useRouter(); // Use router for navigation
const { showError, showSuccess } = useSnackbar();

const serverSlug = route.params.slug as string;
const isLoading = ref(true);
const deleteDialog = ref(false);
const showUploadLogoDialog = ref(false); // State for the logo upload dialog

const isAuthenticated = computed(() => $auth.check());
const isAuthenticatedRef = toRef(isAuthenticated); // Create Ref for composable
const isSecurityOrAdmin = computed(() => $auth.isSecurityOrAdmin());

const server = ref<Server | null>(null);
const serverRef = toRef(server); // Create Ref for composable

// --- Initialize Subscription Composable ---
const subscriptionComposable = useSubscription(serverRef, isAuthenticatedRef);
// Expose necessary state/methods from composable for template
const { subscribe, handleHeadersSave } = subscriptionComposable;
// Rename composable's showHeadersDialog to avoid conflict if needed, or use it directly
const showSubscriptionHeadersDialog = subscriptionComposable.showHeadersDialog;

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

// Check if server requires subscription
const requiresSubscription = computed(() => {
  const availability = server.value?.availability;
  console.log(
    `[pages/servers/[slug]] requiresSubscription computed: server.value?.availability = ${availability}`
  );
  return availability === "SUBSCRIPTION";
});

// Check if user is subscribed AND the server has a template
const isSubscribedAndHasTemplate = computed(() => {
  // Safely access the template property and check its length
  const template = (
    server.value as Server & {
      subscriptionHeaderTemplate?: SubscriptionHeaderTemplateItem[];
    }
  )?.subscriptionHeaderTemplate;
  return (
    subscriptionComposable.subscriptionState.isSubscribed && // Use composable state
    template &&
    template.length > 0
  );
});

// Get Gateway Base URL from settings, default to window origin on client
const gatewayBaseUrl = computed(() => {
  const settingUrl = $settings.get("general_gateway_address") as
    | string
    | undefined;
  if (settingUrl) {
    return settingUrl;
  }
  // Fallback to current origin if running on client
  if (import.meta.client) {
    return window.location.origin;
  }
  // Default fallback (should ideally not be reached if setting is configured)
  return "http://gate4.ai";
});

onMounted(async () => {
  await fetchServer();
});

async function fetchServer() {
  isLoading.value = true;
  server.value = null; // Reset server data before fetch

  try {
    // Fetch server using the SLUG, include template info
    // Type assertion includes the optional template property
    const fetchedServerData = await $api.getJson<
      Server & { subscriptionHeaderTemplate?: SubscriptionHeaderTemplateItem[] }
    >(`/servers/${serverSlug}`);

    server.value = fetchedServerData; // Update the ref, composable will react
  } catch (error: unknown) {
    // Use unknown instead of any
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
  showUploadLogoDialog.value = true;
}

function openDeleteDialog() {
  deleteDialog.value = true;
}

async function deleteServer() {
  if (!server.value) return;
  try {
    // Use $api.deleteJson (Ensure this method exists in your $api plugin definition)
    await $api.deleteJson(`/servers/${serverSlug}`);
    showSuccess("Server deleted successfully.");
    router.push("/servers"); // Navigate back to catalog after delete
  } catch (error: unknown) {
    // Use unknown instead of any
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
  // The composable will automatically update its state via the watcher
  fetchServer();
}

// Handle header save from SubscriptionHeaderValuesDialog
async function onSubscriptionHeadersSaved(
  headerValues: Record<string, string>
) {
  // Call the composable's handler which performs the subscription
  const success = await handleHeadersSave(headerValues);
  if (success) {
    // Optionally refetch data if needed, but dialog already saved.
    // Composables state should be updated.
    console.log("Subscription headers saved and subscription completed.");
    // fetchServer(); // Might be redundant if state updates are sufficient
  } else {
    showError("Failed to complete subscription after saving headers.");
  }
}

// Handler for when the logo is successfully updated via UploadServerLogoDialog
function handleLogoUpdate(newImageUrl: string) {
  if (server.value) {
    server.value.imageUrl = newImageUrl; // Update local data to refresh image instantly
  }
  // No need to close dialog here, component does it
}

// --- Updated Subscription Trigger Function ---
// Function to trigger the subscription flow using the composable
async function triggerSubscriptionFlow() {
  console.log("Triggering subscription flow via [slug].vue");
  const success = await subscribe(); // Call the composable's subscribe method
  // If subscribe() returns true, it means the process started successfully (might involve dialog)
  // If it returns false, it means it was blocked (e.g., not authenticated, permissions error)
  if (success) {
    // If the process started and didn't immediately fail (e.g. permissions),
    // we might need to refresh server data if the subscription happened directly
    // or if the dialog was involved and finished successfully.
    // Re-fetch after a short delay to allow state update or dialog interaction.
    // This is a simpler approach than tracking the dialog's promise resolution here.
    setTimeout(() => {
      fetchServer();
    }, 100); // Short delay
  }
}

// --- LOGGING FUNCTION ---
function logConnectionInstructionProps(props: {
  isAuthenticated: boolean;
  requiresSubscription: boolean;
  isSubscribedFromComposable: boolean;
  isSubscribedFromServer: boolean;
  hasImplicitAccess: boolean;
}) {
  console.log(
    `[pages/servers/[slug]] Props being passed to ServerConnectionInstructions:`,
    props
  );
  return ""; // Return empty string so it doesn't render anything
}
// --- END LOGGING FUNCTION ---

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
.bg-opacity-50 {
  background-color: rgba(0, 0, 0, 0.5);
}
</style>
