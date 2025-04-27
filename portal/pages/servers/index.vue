////////////////////////// //
/home/alex/go-ai/gate4ai/www/pages/servers/index.vue //////////////////////////
<template>
  <div>
    <div class="d-flex justify-space-between align-center mb-6">
      <!-- Dynamically update title based on filter -->
      <h1 class="text-h3">{{ pageTitle }}</h1>

      <AddServerButton
        :is-authenticated="isAuthenticated"
        @open-add-dialog="showAddServerDialog = true"
      />
    </div>

    <!-- Search and Filter (remains the same) -->
    <ServerSearch v-model="searchQuery" />

    <!-- Loading State -->
    <div v-if="isLoading" class="d-flex justify-center py-12">
      <v-progress-circular indeterminate color="primary" />
    </div>

    <!-- Provider Cards -->
    <v-row v-else-if="filteredServers.length > 0">
      <v-col
        v-for="server in filteredServers"
        :key="server.id"
        cols="12"
        md="6"
        lg="4"
      >
        <ServerCard
          :server="server"
          :is-authenticated="isAuthenticated"
          @subscribe="handleSubscriptionUpdate"
        />
      </v-col>
    </v-row>

    <!-- Empty State -->
    <v-row v-else>
      <v-col cols="12" class="text-center py-12">
        <v-icon size="x-large" color="grey">mdi-database-off</v-icon>
        <!-- Dynamic empty state message -->
        <h3 class="text-h5 mt-4 mb-2">No servers found</h3>
        <p class="text-body-1">{{ emptyStateMessage }}</p>
        <!-- Suggest adding server only if viewing 'owned' and list is empty -->
        <v-btn
          v-if="filterType === 'owned'"
          variant="text"
          color="primary"
          class="ml-2"
          @click="showAddServerDialog = true"
        >
          Add Your First Server
        </v-btn>
        <!-- Suggest browsing if viewing 'subscribed' and list is empty -->
        <v-btn
          v-else-if="filterType === 'subscribed'"
          variant="text"
          color="primary"
          to="/servers"
          class="ml-2"
        >
          Browse Catalog
        </v-btn>
      </v-col>
    </v-row>

    <!-- Add Server Dialog (remains the same) -->
    <AddServerDialog
      v-model="showAddServerDialog"
      @server-added="onServerAdded"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from "vue";
import { useRoute } from "vue-router"; // Import useRoute
import AddServerDialog from "~/components/AddServerDialog.vue";
import ServerCard from "~/components/ServerCard.vue";
import ServerSearch from "~/components/ServerSearch.vue";
import type { Server } from "~/utils/server";
import { useSnackbar } from "~/composables/useSnackbar";
import AddServerButton from "~/components/AddServerButton.vue";

const { $auth, $settings, $api } = useNuxtApp();
const { showError } = useSnackbar(); // Use snackbar for errors
const route = useRoute(); // Get route object

const servers = ref<Server[]>([]);
const searchQuery = ref("");
const isLoading = ref(true);
const showAddServerDialog = ref(false);

const isAuthenticated = computed(() => $auth.check());
const userRole = computed(() => $auth.getUser()?.role);

// Determine filter type from query parameter
const filterType = computed(() => route.query.filter as string | undefined);

// Dynamic page title based on filter
const pageTitle = computed(() => {
  switch (filterType.value) {
    case "subscribed":
      return "Subscribed Servers";
    case "owned":
      return "Published Servers";
    default:
      return "Server Catalog";
  }
});

// Dynamic empty state message
const emptyStateMessage = computed(() => {
  if (searchQuery.value) {
    return "No servers match your search query.";
  }
  switch (filterType.value) {
    case "subscribed":
      return "You haven't subscribed to any servers yet.";
    case "owned":
      return "You haven't published any servers yet.";
    default:
      return "There are currently no servers in the catalog.";
  }
});

// Fetch servers on component mount and when filter changes
onMounted(() => {
  fetchServers();
});

// Watch for changes in the query parameter and refetch
watch(
  filterType,
  () => {
    searchQuery.value = ""; // Reset search when filter changes
    fetchServers();
  },
  { immediate: false }
); // Don't run immediately, onMounted handles initial load

async function fetchServers() {
  isLoading.value = true;
  try {
    // Pass the filter to the API call
    const filterQuery = filterType.value ? `?filter=${filterType.value}` : "";
    servers.value = await $api.getJson(`/servers${filterQuery}`);
  } catch (error: unknown) {
    console.error("Error fetching servers:", error);
    // Show error message using snackbar
    if (error instanceof Error) {
      showError(error.message);
    } else {
      showError("Failed to load servers.");
    }
    servers.value = []; // Clear servers on error
  } finally {
    isLoading.value = false;
  }
}

// Filter servers based on search query (client-side)
const filteredServers = computed(() => {
  let result = [...servers.value];

  if (searchQuery.value) {
    const query = searchQuery.value.toLowerCase();
    result = result.filter(
      (server) =>
        server.name.toLowerCase().includes(query) ||
        server.description?.toLowerCase().includes(query)
    );
  }
  return result;
});

// Handler for when a new server is added via the dialog
function onServerAdded(newServer: Server) {
  // Only add to the list if the current filter is 'all' or 'owned'
  // If viewing 'subscribed', adding a server shouldn't show it here immediately.
  if (!filterType.value || filterType.value === "owned") {
    servers.value.push(newServer);
  }
  // Optionally: refetch if needed, but push is usually sufficient
  // fetchServers();
}

// Handler for subscription updates from ServerCard
function handleSubscriptionUpdate(payload: {
  serverId: string;
  isSubscribed: boolean;
}) {
  // If the user unsubscribes while viewing the 'subscribed' list, remove the server immediately
  if (filterType.value === "subscribed" && !payload.isSubscribed) {
    servers.value = servers.value.filter((s) => s.id !== payload.serverId);
  }
  // If viewing 'all' or 'owned', update the specific server's subscription status in the local list
  else if (!filterType.value || filterType.value === "owned") {
    const serverIndex = servers.value.findIndex(
      (s) => s.id === payload.serverId
    );
    if (serverIndex !== -1) {
      servers.value[serverIndex] = {
        ...servers.value[serverIndex],
        isCurrentUserSubscribed: payload.isSubscribed,
        // Update subscriptionId if available in payload
        subscriptionId: payload.isSubscribed
          ? (payload as { subscriptionId?: string }).subscriptionId
          : undefined, // Using a more specific type instead of any
      };
    }
  }
  // No immediate visual update needed if user subscribes while on 'subscribed' list (fetchServers will handle it on next load if needed)
}

// Update Nuxt page meta based on the dynamic title
useHead({
  title: computed(() => pageTitle.value), // Make title reactive
});
</script>
