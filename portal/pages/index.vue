<template>
  <div>
    <!-- Hero Section -->
    <v-row class="hero-section py-12">
      <v-col cols="12" md="6" class="d-flex flex-column justify-center">
        <h2 class="text-h4 font-weight-bold">Catalog:</h2>
        <!-- Loading/Error State for Carousel -->
        <div v-if="isLoadingServers" class="d-flex justify-center align-center my-8" style="min-height: 200px;">
          <v-progress-circular indeterminate color="primary" />
        </div>
        <v-alert v-else-if="fetchServersError" type="error" variant="tonal" class="my-4">
          {{ fetchServersError }}
        </v-alert>

        <!-- Server Carousel/Slider -->
        <v-slide-group v-else-if="publicServers.length > 0" show-arrows class="mb-6">
          <v-slide-group-item v-for="server in publicServers" :key="server.id">
            <ServerCard
              :server="server"
              :is-authenticated="isAuthenticated"
              class="ma-3"
              style="width: 300px;"
            />
          </v-slide-group-item>
        </v-slide-group>
        <p v-else class="text-body-1 mb-6">No public servers available in the catalog yet.</p>

        <div class="mt-4">
           <v-btn color="primary" class="mr-4" to="/servers">
            Explore Catalog
          </v-btn>

          <AddServerButton :is-authenticated="isAuthenticated" @open-add-dialog="showAddServerDialog = true" />
        </div>
      </v-col>
      <v-col cols="12" md="6" class="d-flex align-center justify-center">
        <v-img
          src="/images/logo.svg"
          alt="gate4.ai"
          width="200"
          height="200"
          contain
        />
      </v-col>
    </v-row>

    <!-- Add Server Dialog -->
    <AddServerDialog
      v-model="showAddServerDialog"
      @server-added="onServerAdded"
    />

    <v-row class="py-12">
      <v-col cols="12" class="text-center mb-8">
        <h2 class="text-h4 font-weight-bold">Features for Centralized AI Integration Management</h2>
      </v-col>      

      <!-- Feature Row 1 -->
      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-receipt-text-clock-outline</v-icon>
            Cost Management & Quotas
          </v-card-title>
          <v-card-text>
            Granular control over A2A/MCP expenses. Set request, token, or cost quotas per user/project/team with real-time monitoring.
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-shield-lock-outline</v-icon>
            Centralized Security & Policy
          </v-card-title>
          <v-card-text>
            Define and enforce security rules centrally. Manage provider allow/deny lists, role-based access control (RBAC), and model usage policies.
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-magnify-scan</v-icon>
            Auditing & Monitoring
          </v-card-title>
          <v-card-text>
            Detailed logging of all interactions (who, what, when, provider, result). Generate usage reports for compliance and insights.
          </v-card-text>
        </v-card>
      </v-col>

      <!-- Feature Row 2 -->
       <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-lan-connect</v-icon>
            Unified Provider Management
          </v-card-title>
          <v-card-text>
             Connect, configure, and manage credentials for multiple A2A/MCP providers through a single, unified gateway interface.
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-speedometer</v-icon>
            Rate Limiting & Throttling
          </v-card-title>
          <v-card-text>
            Prevent abuse and manage load by setting request frequency limits (req/sec, req/min) per user, API key, or provider.
          </v-card-text>
        </v-card>
      </v-col>

       <v-col cols="12" md="4">
        <v-card height="100%">
          <v-card-title class="text-h5">
            <v-icon size="large" color="primary" class="mr-2">mdi-playlist-plus</v-icon>
            Request Enrichment
          </v-card-title>
          <v-card-text>
            Inject contextual data (like user IDs, roles, app keys) into requests to enable finer-grained agent decisions and access control.
          </v-card-text>
        </v-card>
      </v-col>

      <v-col cols="12" class="text-center">
        
        <h2 class="text-h4 font-weight-bold mb-3">
          <v-icon color="primary" size="x-large" class="mb-4">mdi-cloud-sync-outline</v-icon>  Use in the Cloud or Install On-Premise
        </h2>
        <p class="text-body-1 text-medium-emphasis">
          Choose the deployment option that best suits your needs – leverage our hosted solution or deploy gate4.ai within your own infrastructure.
        </p>
      </v-col>
    </v-row>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import ServerCard from '~/components/ServerCard.vue';
import AddServerButton from '~/components/AddServerButton.vue';
import AddServerDialog from '~/components/AddServerDialog.vue';
import type { ServerInfo } from '~/utils/server';
import { useSnackbar } from '~/composables/useSnackbar';

const { $auth, $api } = useNuxtApp();
const { showError } = useSnackbar();

const isLoadingServers = ref(true);
const fetchServersError = ref<string | null>(null);
const publicServers = ref<ServerInfo[]>([]);
const showAddServerDialog = ref(false);

const isAuthenticated = computed(() => $auth.check());

definePageMeta({
  title: 'Home',
  layout: 'default',
});

onMounted(() => {
  fetchPublicServers();
});

async function fetchPublicServers() {
  isLoadingServers.value = true;
  fetchServersError.value = null;
  try {
    const allServers = await $api.getJson<ServerInfo[]>('/servers');
    publicServers.value = allServers;
  } catch (error) {
    fetchServersError.value = error instanceof Error ? error.message : 'Failed to load servers.';
    showError(fetchServersError.value);
  } finally {
    isLoadingServers.value = false;
  }
}

function onServerAdded(_newServer: ServerInfo) {
  fetchPublicServers();
}
</script>

<style scoped>
.hero-section {
  background: linear-gradient(135deg, #f5f7fa 0%, #c3cfe2 100%);
}
</style> 