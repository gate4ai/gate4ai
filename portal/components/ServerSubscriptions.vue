<template>
  <div>
    <h3 class="text-h6 mb-2">
      <v-btn variant="text" color="primary" :to="`/servers/${serverSlug}/subscriptions`">
        Subscriptions
        <v-icon class="ml-1">mdi-arrow-right</v-icon>
      </v-btn>
    </h3>

    <!-- Display counts directly from props -->
    <div v-if="counts">
      <v-list>
        <!-- Iterate over the passed counts object -->
        <v-list-item v-for="(count, status) in counts" :key="status">
          <v-list-item-title>
            {{ formatStatus(status) }}: {{ count }}
          </v-list-item-title>
        </v-list-item>

        <!-- Show if counts object is empty -->
        <v-list-item v-if="Object.keys(counts).length === 0">
          <v-list-item-title>No subscriptions yet</v-list-item-title>
        </v-list-item>
      </v-list>
    </div>
    <!-- Show message if counts are not available (e.g., user doesn't have permission) -->
    <div v-else>
       <v-list-item>
         <v-list-item-title class="text-grey">Subscription details available to owners.</v-list-item-title>
       </v-list-item>
    </div>
    <!-- Removed v-progress-circular as loading is handled by parent -->
  </div>
</template>

<script setup lang="ts">
// Added serverSlug prop
const _props = defineProps<{
  serverId: string; // Keep ID if needed for internal actions
  serverSlug: string; // Add slug for navigation
  counts?: Record<string, number>; // Receive counts directly
}>();

// Format subscription status for display (remains the same)
function formatStatus(status: string): string {
  const statusMap: Record<string, string> = {
    'PENDING': 'Pending',
    'ACTIVE': 'Active',
    'BLOCKED': 'Blocked'
  };
  return statusMap[status] || status;
}
</script>