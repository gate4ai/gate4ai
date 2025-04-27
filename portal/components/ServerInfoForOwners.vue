<template>
  <v-card v-if="showForOwners">
    <v-card-title>Information hidden from users</v-card-title>
    <v-card-text>
      <v-list>
        <v-list-item>
          <template #prepend>
            <v-icon color="primary">mdi-check-circle</v-icon>
          </template>
          <v-list-item-title>
            Status: {{ formatServerStatus(server.status) }}
          </v-list-item-title>
        </v-list-item>

        <v-list-item>
          <template #prepend>
            <v-icon color="primary">mdi-access-point</v-icon>
          </template>
          <v-list-item-title>
            Availability: {{ formatServerAvailability(server.availability) }}
          </v-list-item-title>
        </v-list-item>

        <v-list-item v-if="server.serverUrl">
          <template #prepend>
            <v-icon color="primary">mdi-server</v-icon>
          </template>
          <v-list-item-title>
            Server URL: {{ server.serverUrl }}
          </v-list-item-title>
        </v-list-item>

        <!-- Display Server Type -->
        <v-list-item v-if="server.protocol">
          <template #prepend>
            <v-icon color="primary">mdi-protocol</v-icon>
          </template>
          <v-list-item-title>
            Server Protocol: {{ server.protocol }}
          </v-list-item-title>
        </v-list-item>

        <!-- Display Protocol Version -->
        <v-list-item v-if="server.protocolVersion">
          <template #prepend>
            <v-icon color="primary">mdi-version</v-icon>
          </template>
          <v-list-item-title>
            Protocol Version: {{ server.protocolVersion }}
          </v-list-item-title>
        </v-list-item>

        <!-- Display Server Slug -->
        <v-list-item v-if="server.slug">
          <template #prepend>
            <v-icon color="primary">mdi-link-variant</v-icon>
          </template>
          <v-list-item-title> Slug: {{ server.slug }} </v-list-item-title>
        </v-list-item>
      </v-list>

      <!-- Owners Section -->
      <ServerOwners
        v-if="server.owners"
        :server-id="server.id"
        :owners="server.owners"
        class="mt-4"
      />

      <!-- Subscriptions Section - Pass the counts down -->
      <ServerSubscriptions
        v-if="server.id && server.slug"
        :server-id="server.id"
        :server-slug="server.slug"
        :counts="server.subscriptionStatusCounts"
        class="mt-4"
      />
    </v-card-text>
  </v-card>
</template>

<script setup lang="ts">
import { computed } from "vue";
// Make sure the imported Server type includes subscriptionStatusCounts, slug, protocol, protocolVersion
import type { Server } from "~/utils/server"; // Update import
import type { ServerStatus, ServerAvailability } from "@prisma/client"; // Import from Prisma
import ServerOwners from "./ServerOwners.vue";
import ServerSubscriptions from "./ServerSubscriptions.vue";

const props = defineProps<{
  server: Server; // Type should now include optional subscriptionStatusCounts, slug, type
  isAuthenticated: boolean;
}>();

const { $auth } = useNuxtApp();

// Check if the component should be shown (only for owners, admins, and security)
const showForOwners = computed(() => {
  if (!props.isAuthenticated) return false;

  const user = $auth.getUser();
  if (!user) return false;

  // Show for admins and security
  if ($auth.isSecurityOrAdmin()) return true;

  // Show for owners
  // Use optional chaining for owners array
  return (
    props.server.owners?.some((owner) => owner.user.id === user.id) || false
  );
});

// Format server status for display
function formatServerStatus(status: ServerStatus | undefined): string {
  // Use enum type
  if (!status) return "Unknown";
  const statusMap: Record<ServerStatus, string> = {
    DRAFT: "Draft",
    ACTIVE: "Active",
    BLOCKED: "Blocked",
  };
  return statusMap[status] || status;
}

// Format server availability for display
function formatServerAvailability(
  availability: ServerAvailability | undefined
): string {
  // Use enum type
  if (!availability) return "Unknown";
  const availabilityMap: Record<ServerAvailability, string> = {
    PUBLIC: "Public",
    PRIVATE: "Private",
    SUBSCRIPTION: "Subscription",
  };
  return availabilityMap[availability] || availability;
}
</script>
