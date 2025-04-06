<template>
  <v-card class="h-100">
    <v-img
      :src="server.imageUrl || '/images/default-server.svg'"
      height="200"
      cover
      class="align-end"
    >
      <v-card-title class="text-white bg-black bg-opacity-50 w-100">
        {{ server.name }}
      </v-card-title>
    </v-img>
    
    <v-card-text>
      <p class="mb-4">{{ server.description }}</p>
      
      Tools:
      <v-chip-group>
        <v-chip
          v-for="tool in (server.tools || []).slice(0, 3)"
          :key="tool.id"
          color="primary"
          size="small"
        >
          {{ tool.name }}
        </v-chip>
        <v-chip
          v-if="server.tools && server.tools.length > 3"
          color="grey"
          size="small"
        >
          +{{ server.tools.length - 3 }} more
        </v-chip>
      </v-chip-group>
    </v-card-text>
    
    <v-card-actions>
      <v-btn
        variant="text"
        color="primary"
        :to="`/servers/${server.id}`"
      >
        View Details
      </v-btn>
      <v-spacer/>
      <SubscribeButton
        :server="server"
        :is-authenticated="isAuthenticated"
        @update:subscription="handleSubscriptionUpdate"
      />
    </v-card-actions>
  </v-card>
</template>

<script setup lang="ts">
import type { Server } from '~/utils/server';

defineProps<{
  server: Server;
  isAuthenticated: boolean;
}>();

const emit = defineEmits<{
  (e: 'subscribe', payload: { serverId: string, isSubscribed: boolean, subscriptionId?: string }): void;
}>();

function handleSubscriptionUpdate(payload: { serverId: string; isSubscribed: boolean; subscriptionId?: string }) {
  // Just forward the subscription update to parent
  emit('subscribe', payload);
}
</script>