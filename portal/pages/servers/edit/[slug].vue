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
      <v-card v-else-if="serverDataForForm" class="pa-4 mb-6">
        <v-card-title class="text-h5 mb-3">Core Details</v-card-title>
        <ServerForm
          :server-data="serverDataForForm"
          :is-submitting="isSubmitting"
          submit-label="Update Core Details"
          @submit="updateServer"
          @cancel="navigateBack"
        />
      </v-card>

      <!-- NEW: Headers Management Section -->
      <v-card v-if="serverDataForForm && canManageServer" class="pa-4 mb-6">
        <v-card-title class="text-h5 mb-3">Server Headers</v-card-title>
        <p class="text-caption mb-4">
          Headers automatically sent by the gateway to this server.
        </p>
        <v-btn color="secondary" @click="showServerHeadersDialog = true">
          Manage Server Headers
        </v-btn>
      </v-card>

      <!-- NEW: Subscription Header Template Management Section -->
      <v-card v-if="serverDataForForm && canManageServer" class="pa-4 mb-6">
        <v-card-title class="text-h5 mb-3"
          >Subscription Header Template</v-card-title
        >
        <p class="text-caption mb-4">
          Define headers subscribers must provide.
        </p>
        <v-btn color="secondary" @click="showSubscriptionTemplateDialog = true">
          Manage Subscription Template
        </v-btn>
      </v-card>

      <!-- Error/Not Found State -->
      <div v-else class="text-center py-10">
        <v-alert type="error" variant="tonal">
          {{ loadError || "Server not found or could not be loaded." }}
        </v-alert>
        <v-btn color="primary" :to="`/servers/${serverSlug}`" class="mt-4"
          >Back to Server Details</v-btn
        >
      </div>

      <!-- Dialogs for Headers -->
      <ServerHeadersForm
        v-if="showServerHeadersDialog"
        v-model="showServerHeadersDialog"
        :server-slug="serverSlug"
        :server-url="serverDataForForm?.serverUrl || ''"
        :initial-headers="currentServerHeaders"
        @headers-updated="onServerHeadersUpdated"
      />

      <SubscriptionHeaderTemplateForm
        v-if="showSubscriptionTemplateDialog"
        v-model="showSubscriptionTemplateDialog"
        :server-slug="serverSlug"
        :server-url="serverDataForForm?.serverUrl || ''"
        :initial-template="currentSubscriptionTemplate"
        @template-updated="onSubscriptionTemplateUpdated"
      />
    </v-container>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from "vue";
import { useRoute } from "vue-router";
import ServerForm from "~/components/ServerForm.vue";
import ServerHeadersForm from "~/components/ServerHeadersForm.vue"; // NEW
import SubscriptionHeaderTemplateForm from "~/components/SubscriptionHeaderTemplateForm.vue"; // NEW
import type { ServerData } from "~/utils/server";
import { useSnackbar } from "~/composables/useSnackbar";
import type {
  ServerStatus,
  ServerAvailability,
  ServerProtocol,
} from "@prisma/client";

// Interfaces for headers and template
interface HeaderRecord {
  [key: string]: string;
}
interface TemplateItem {
  id?: string;
  key: string;
  description?: string | null;
  required: boolean;
}

interface ServerApiResponse {
  id: string;
  name: string;
  slug: string;
  protocol: ServerProtocol;
  protocolVersion?: string;
  description?: string | null;
  website?: string | null;
  email?: string | null;
  imageUrl?: string | null;
  serverUrl: string;
  status: ServerStatus;
  availability: ServerAvailability;
  owners: { user: { id: string } }[]; // Need owners for permission check
}

const { $auth, $api } = useNuxtApp();
const route = useRoute();
const serverSlug = route.params.slug as string;
const isLoading = ref(true);
const isSubmitting = ref(false); // For core details form
const { showError, showSuccess } = useSnackbar();
const loadError = ref<string | null>(null);

const serverDataForForm = ref<ServerData | null>(null);
const currentServerHeaders = ref<HeaderRecord>({}); // NEW
const currentSubscriptionTemplate = ref<TemplateItem[]>([]); // NEW
const showServerHeadersDialog = ref(false); // NEW
const showSubscriptionTemplateDialog = ref(false); // NEW

const isAuthenticated = computed(() => $auth.check());
const currentUser = computed(() => $auth.getUser());

// Computed permission check (simplified)
const canManageServer = computed(() => {
  if (!isAuthenticated.value || !serverDataForForm.value) return false;
  const user = currentUser.value;
  if (!user) return false;
  if (user.role === "ADMIN" || user.role === "SECURITY") return true;
  // Need to fetch owners during fetchServer to check ownership here
  // For now, assume true if not admin/security (adjust after fetching owners)
  // This will be corrected in fetchServer
  return true; // Placeholder - will be updated after fetch
});

onMounted(async () => {
  if (!isAuthenticated.value) {
    navigateTo(`/login?redirect=${route.fullPath}`);
    return;
  }
  await fetchServer(); // Fetch core details
  // Fetch headers and template after core details are loaded
  if (serverDataForForm.value) {
    await fetchServerHeaders();
    await fetchSubscriptionTemplate();
  }
});

async function fetchServer() {
  isLoading.value = true;
  loadError.value = null;
  serverDataForForm.value = null;

  try {
    // Fetch using slug, include owners for permission check
    const data = await $api.getJson<
      ServerApiResponse & { owners: { user: { id: string } }[] }
    >(`/servers/${serverSlug}`);

    // --- Permission Check ---
    const user = currentUser.value;
    if (!user) throw createError({ statusCode: 401 }); // Should not happen due to middleware/guard

    const isOwner = data.owners?.some((owner) => owner.user?.id === user.id);
    const isAdminOrSecurity = user.role === "ADMIN" || user.role === "SECURITY";

    if (!isOwner && !isAdminOrSecurity) {
      showError("You do not have permission to edit this server.");
      navigateTo(`/servers/${serverSlug}`); // Redirect back to view
      return;
    }
    // --- End Permission Check ---

    serverDataForForm.value = {
      id: data.id,
      name: data.name,
      slug: data.slug,
      protocol: data.protocol,
      protocolVersion: data.protocolVersion || "",
      description: data.description || "",
      website: data.website || "",
      email: data.email || "",
      imageUrl: data.imageUrl || "",
      serverUrl: data.serverUrl,
      status: data.status,
      availability: data.availability,
    };
  } catch (err: unknown) {
    console.error("Error fetching server details for edit:", err);
    const message =
      err instanceof Error
        ? err.message
        : "Failed to load server details for editing.";
    loadError.value = message;
    showError(message);
  } finally {
    isLoading.value = false;
  }
}

// --- NEW: Fetch Server Headers ---
async function fetchServerHeaders() {
  try {
    currentServerHeaders.value = await $api.getJson<HeaderRecord>(
      `/servers/${serverSlug}/headers`
    );
  } catch (err) {
    console.error("Failed to load server headers:", err);
    showError("Could not load server headers.");
  }
}

// --- NEW: Fetch Subscription Template ---
async function fetchSubscriptionTemplate() {
  try {
    currentSubscriptionTemplate.value = await $api.getJson<TemplateItem[]>(
      `/servers/${serverSlug}/subscription-header-template`
    );
  } catch (err) {
    console.error("Failed to load subscription template:", err);
    showError("Could not load subscription header template.");
  }
}

// Update server CORE details
function updateServer(updatedData: ServerData) {
  if (!updatedData) return;
  isSubmitting.value = true;
  const payload = {
    name: updatedData.name,
    protocol: updatedData.protocol,
    protocolVersion: updatedData.protocolVersion || "",
    description: updatedData.description || null,
    website: updatedData.website || null,
    email: updatedData.email || null,
    imageUrl: updatedData.imageUrl || null,
    serverUrl: updatedData.serverUrl,
    status: updatedData.status,
    availability: updatedData.availability,
  };

  $api
    .putJson(`/servers/${serverSlug}`, payload)
    .then(() => {
      showSuccess("Server core details updated successfully");
      // Optionally refetch data or navigate back
      // navigateTo(`/servers/${serverSlug}`);
    })
    .catch((err: unknown) => {
      const message =
        err instanceof Error ? err.message : "Failed to update server.";
      showError(message);
      console.error("Error updating server:", err);
    })
    .finally(() => {
      isSubmitting.value = false;
    });
}

// --- NEW: Handlers for Dialog Updates ---
function onServerHeadersUpdated(newHeaders: HeaderRecord) {
  currentServerHeaders.value = newHeaders; // Update local state
}

function onSubscriptionTemplateUpdated(newTemplate: TemplateItem[]) {
  currentSubscriptionTemplate.value = newTemplate; // Update local state
}

// Navigate back
function navigateBack() {
  navigateTo(`/servers/${serverSlug}`);
}

useHead({
  title: computed(() => `Edit ${serverDataForForm.value?.name || "Server"}`),
});
</script>
