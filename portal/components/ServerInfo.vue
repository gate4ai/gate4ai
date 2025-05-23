<template>
  <v-card>
    <v-card-title>Server Information</v-card-title>
    <v-card-text>
      <v-list>
        <v-list-item v-if="server.website">
          <template #prepend>
            <v-icon color="primary">mdi-web</v-icon>
          </template>
          <v-list-item-title>
            <a :href="server.website" target="_blank" rel="noopener noreferrer">
              {{ server.website }}
            </a>
          </v-list-item-title>
        </v-list-item>

        <v-list-item v-if="server.email && showOwnerEmail">
          <template #prepend>
            <v-icon color="primary">mdi-email</v-icon>
          </template>
          <v-list-item-title>
            <a :href="`mailto:${server.email}`">
              {{ server.email }}
            </a>
          </v-list-item-title>
        </v-list-item>

        <v-list-item>
          <template #prepend>
            <v-icon color="primary">mdi-account-group</v-icon>
          </template>
          <v-list-item-title>
            {{ server._count?.subscriptions || 0 }} subscribers
          </v-list-item-title>
        </v-list-item>

        <v-list-item v-if="server.protocol">
          <template #prepend>
            <v-icon color="primary">mdi-protocol</v-icon>
          </template>
          <v-list-item-title>
            Protocol: {{ server.protocol }} - {{ server.protocolVersion }}
          </v-list-item-title>
        </v-list-item>
      </v-list>
    </v-card-text>

    <v-card-actions>
      <SubscribeButton
        :server="server"
        :is-authenticated="isAuthenticated"
        @update:subscription="handleSubscriptionUpdate"
      />
    </v-card-actions>
  </v-card>
</template>

<script setup lang="ts">
import type { Server } from "~/utils/server";
import { useRuntimeConfig } from "#app";
import { computed } from "vue";

const _props = defineProps<{
  server: Server;
  isAuthenticated: boolean;
}>();

const emit = defineEmits<{
  (e: "subscribe"): void;
}>();

const config = useRuntimeConfig();
const showOwnerEmail = computed(
  () => config.public.settings?.show_owner_email || false
);

function handleSubscriptionUpdate() {
  // Emit event to parent to handle subscription update
  emit("subscribe");
}
</script>
