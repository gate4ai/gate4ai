<template>
  <div>
    <div class="d-flex align-center mb-4">
      <v-btn icon class="mr-2" @click="navigateBack">
        <v-icon>mdi-arrow-left</v-icon>
      </v-btn>
      <h1 class="text-h4">Subscriptions for {{ serverName || 'Server' }}</h1>
    </div>

    <!-- Loading State -->
    <div v-if="isLoading" class="d-flex justify-center py-12">
      <v-progress-circular indeterminate color="primary"/>
    </div>

     <!-- Error State -->
     <v-alert v-else-if="loadError" type="error" class="my-4" variant="tonal">
        {{ loadError }}
        <div class="mt-2">
             <v-btn color="primary" variant="text" @click="fetchData">Retry</v-btn>
             <v-btn variant="text" @click="navigateBack">Back to Server</v-btn>
         </div>
     </v-alert>

    <!-- Subscriptions Table -->
    <div v-else-if="subscriptions.length > 0">
      <v-table>
        <thead>
          <tr>
            <th>ID</th>
            <th v-if="canViewUserDetails">User</th>
            <th v-if="canViewUserEmail">Email</th>
            <th>Status</th>
            <th v-if="canManageSubscriptions">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="subscription in subscriptions" :key="subscription.id">
            <td>{{ subscription.id }}</td>
            <td v-if="canViewUserDetails">{{ subscription.user?.name || 'Unknown' }}</td>
            <td v-if="canViewUserEmail">{{ subscription.user?.email || 'Unknown' }}</td>
            <td>
              <v-chip
                :color="getStatusColor(subscription.status)"
                text-color="white"
                size="small"
              >
                {{ subscription.status }}
              </v-chip>
            </td>
            <td v-if="canManageSubscriptions">
              <v-menu>
                <template #activator="{ props }">
                  <v-btn
                    icon
                    v-bind="props"
                    variant="text"
                    size="small"
                    color="primary"
                  >
                    <v-icon>mdi-dots-vertical</v-icon>
                  </v-btn>
                </template>
                <v-list density="compact">
                  <v-list-item
                    v-for="status in ['ACTIVE', 'BLOCKED', 'PENDING']"
                    :key="status"
                    :disabled="subscription.status === status || isUpdating[subscription.id]"
                    @click="updateSubscriptionStatus(subscription.id, status)"
                  >
                    <v-list-item-title>Set {{ status }}</v-list-item-title>
                  </v-list-item>
                  <!-- Consider adding a delete option here if needed -->
                </v-list>
              </v-menu>
            </td>
          </tr>
        </tbody>
      </v-table>
    </div>

    <!-- Empty State -->
    <v-alert
      v-else
      type="info"
      variant="tonal"
      class="mt-4"
    >
      This server has no subscriptions yet.
    </v-alert>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, reactive } from 'vue';
import { useRoute } from 'vue-router';
import { useSnackbar } from '~/composables/useSnackbar';
import type { SubscriptionStatus } from '@prisma/client'; // Import enum

definePageMeta({
  title: 'Server Subscriptions',
  layout: 'default',
  middleware: ['auth'],
});

// Interfaces
interface SimpleServer {
  id: string;
  name: string;
  owners: { user: { id: string } }[]; // Need owners for permission check
}

interface UserInfo {
  id: string;
  name?: string | null; // Prisma can return null
  email: string;
}

interface Subscription {
  id: string;
  status: SubscriptionStatus; // Use enum
  serverId: string;
  userId: string;
  user?: UserInfo; // Make user optional as API might not always include it
}

// Route and params
const route = useRoute();
const serverSlug = route.params.slug as string; // Get slug from route

// State
const { $auth, $settings, $api } = useNuxtApp();
const isLoading = ref(true);
const loadError = ref<string | null>(null);
const server = ref<SimpleServer | null>(null); // Store basic server info
const subscriptions = ref<Subscription[]>([]);
const isUpdating = reactive<Record<string, boolean>>({}); // Track update status per subscription
const { showError, showSuccess } = useSnackbar();

// Computed Properties
const serverName = computed(() => server.value?.name || serverSlug); // Display slug if name not loaded

const currentUser = computed(() => $auth.getUser());

const isAdminOrSecurity = computed(() => {
  return currentUser.value && (currentUser.value.role === 'ADMIN' || currentUser.value.role === 'SECURITY');
});

const isOwner = computed(() => {
  return currentUser.value && server.value && server.value.owners?.some(owner => owner.user.id === currentUser.value!.id);
});

// Permissions (depend on server data being loaded)
const canViewUserDetails = computed(() => {
  return isOwner.value || isAdminOrSecurity.value;
});

const canViewUserEmail = computed(() => {
  if (isAdminOrSecurity.value) return true;
  // Check setting only if the user is an owner
  if (isOwner.value) {
    // Default to false if setting is not loaded or not explicitly true
    return $settings.get('server_owner_can_see_user_email') === true;
  }
  return false;
});

const canManageSubscriptions = computed(() => {
  // Only owners, admins, or security can manage
  return isOwner.value || isAdminOrSecurity.value;
});

// Functions
function navigateBack() {
  navigateTo(`/servers/${serverSlug}`); // Use slug for navigation
}

function getStatusColor(status: SubscriptionStatus) {
  switch (status) {
    case 'ACTIVE': return 'success';
    case 'BLOCKED': return 'error';
    case 'PENDING': return 'warning';
    default: return 'grey';
  }
}

// Combined fetch function
async function fetchData() {
  isLoading.value = true;
  loadError.value = null; // Reset error
  server.value = null;
  subscriptions.value = [];

  try {
    // Fetch server details (needed for name and permissions) first
    // Use a limited select to avoid fetching too much data
    server.value = await $api.getJson<SimpleServer>(`/servers/${serverSlug}`, {
        query: { select: 'id,name,owners' } // Hypothetical query param, adjust if API supports sparse fieldsets
    });

    // Check permissions *after* fetching server data
    if (!canManageSubscriptions.value) {
       throw createError({ statusCode: 403, statusMessage: 'Forbidden: You do not have permission to view these subscriptions.' });
    }

    // Fetch subscriptions using the SLUG
    subscriptions.value = await $api.getJson<Subscription[]>(`/subscriptions/server/${serverSlug}`);

  } catch (err: unknown) {
    console.error('Error loading subscription data:', err);
     const message = err instanceof Error ? err.message : 'Failed to load subscription data.';
     loadError.value = message;
     showError(message);
  } finally {
    isLoading.value = false;
  }
}


async function updateSubscriptionStatus(subscriptionId: string, status: SubscriptionStatus) {
  const index = subscriptions.value.findIndex(sub => sub.id === subscriptionId);
  if (index === -1) return;

  isUpdating[subscriptionId] = true;

  try {
    // Call the PUT endpoint (uses subscription ID, not server slug)
    const updatedSubscription = await $api.putJson<Subscription>(`/subscriptions/${subscriptionId}`, { status });

    // Update local state reactively
    subscriptions.value[index] = { ...subscriptions.value[index], ...updatedSubscription };
    showSuccess('Subscription status updated successfully!');

  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : 'Failed to update subscription status';
    showError(message);
    console.error("Error updating subscription status:", err);
  } finally {
    isUpdating[subscriptionId] = false;
  }
}

onMounted(async () => {
  // Authentication check is handled by middleware, but double-check anyway
  if (!$auth.check()) {
    navigateTo(`/login?redirect=${route.fullPath}`);
    return;
  }
  await fetchData();
});

// Update Nuxt page meta title dynamically
useHead({
  title: computed(() => `Subscriptions for ${serverName.value}`)
})

</script>