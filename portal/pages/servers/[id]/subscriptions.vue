<template>
  <div>
    <div class="d-flex align-center mb-4">
      <v-btn icon class="mr-2" @click="navigateBack">
        <v-icon>mdi-arrow-left</v-icon>
      </v-btn>
      <h1 class="text-h4">Subscriptions for {{ server?.name || 'Server' }}</h1>
    </div>

    <div v-if="isLoading" class="d-flex justify-center py-12">
      <v-progress-circular indeterminate color="primary"/>
    </div>

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
                    color="primary"
                  >
                    <v-icon>mdi-dots-vertical</v-icon>
                  </v-btn>
                </template>
                <v-list>
                  <v-list-item
                    v-for="status in ['ACTIVE', 'BLOCKED', 'PENDING']"
                    :key="status"
                    :disabled="subscription.status === status"
                    @click="updateSubscriptionStatus(subscription.id, status)"
                  >
                    <v-list-item-title>Set {{ status }}</v-list-item-title>
                  </v-list-item>
                </v-list>
              </v-menu>
            </td>
          </tr>
        </tbody>
      </v-table>
    </div>
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
import { ref, computed, onMounted } from 'vue';
import { useSnackbar } from '~/composables/useSnackbar';

definePageMeta({
  title: 'Server Subscriptions',
  layout: 'default',
  middleware: ['auth'],
});

// Interfaces
interface Server {
  id: string;
  name: string;
}

interface User {
  id: string;
  name?: string;
  email: string;
}

interface Subscription {
  id: string;
  status: 'ACTIVE' | 'BLOCKED' | 'PENDING';
  serverId: string;
  userId: string;
  user?: User;
}

// Route and params
const route = useRoute();
const serverId = route.params.id as string;

// State
const { $auth, $settings } = useNuxtApp();
const isLoading = ref(true);
const server = ref<Server | null>(null);
const subscriptions = ref<Subscription[]>([]);
const { showError, showSuccess } = useSnackbar();

// Computed
const isAdminOrSecurity = computed(() => {
  const user = $auth.getUser();
  return user && (user.role === 'ADMIN' || user.role === 'SECURITY');
});

const isOwner = computed(() => {
  const user = $auth.getUser();
  return user && server.value && server.value.id === user.id;
});

const canViewUserDetails = computed(() => {
  return isAdminOrSecurity.value || isOwner.value;
});

const canViewUserEmail = computed(() => {
  if (isAdminOrSecurity.value) return true;
  if (isOwner.value) {
    return $settings.get('server_owner_can_see_user_email') === true;
  }
  return false;
});

const canManageSubscriptions = computed(() => {
  return isAdminOrSecurity.value || isOwner.value;
});

// Functions
function navigateBack() {
  navigateTo(`/servers/${serverId}`);
}

function getStatusColor(status: string) {
  switch (status) {
    case 'ACTIVE':
      return 'success';
    case 'BLOCKED':
      return 'error';
    case 'PENDING':
      return 'warning';
    default:
      return 'grey';
  }
}

async function fetchServerDetails() {
  try {
    const { $api } = useNuxtApp();
    server.value = await $api.getJson(`/servers/${serverId}`);
  } catch (error) {
    console.error('Error fetching server details:', error);
  }
}

async function fetchSubscriptions() {
  isLoading.value = true;
  
  try {
    const { $api } = useNuxtApp();
    subscriptions.value = await $api.getJson(`/subscriptions/server/${serverId}`);
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('Failed to load subscriptions');
    }
    console.error("Error loading subscriptions:", err);
  } finally {
    isLoading.value = false;
  }
}

async function updateSubscriptionStatus(subscriptionId: string, status: string) {
  // Find the index for potential reactive update
  const index = subscriptions.value.findIndex(sub => sub.id === subscriptionId);
  if (index === -1) return; // Should not happen

  // Optional: Add loading state for the specific row/menu if desired
  // subscriptions.value[index].isLoading = true; // Add isLoading to Subscription interface if needed

  try {
    const { $api } = useNuxtApp();

    // Call the new PUT endpoint
    const updatedSubscription = await $api.putJson(`/subscriptions/${subscriptionId}`, { status });

    // Update the local subscription status reactively
    // Ensure the returned 'updatedSubscription' has the same structure as items in subscriptions.value
    subscriptions.value[index] = { ...subscriptions.value[index], ...updatedSubscription };
    showSuccess('Subscription status updated successfully!');

  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('Failed to update subscription status');
    }
    console.error("Error updating subscription status:", err);
  } finally {
     // Optional: Reset loading state
     // if (subscriptions.value[index]) {
     //    subscriptions.value[index].isLoading = false;
     // }
  }
}

onMounted(async () => {
  // Check if user is authenticated
  if (!$auth.check()) {
    navigateTo('/login?redirect=/servers/' + serverId + '/subscriptions');
    return;
  }
  
  await Promise.all([
    fetchServerDetails(),
    fetchSubscriptions()
  ]);
});
</script> 