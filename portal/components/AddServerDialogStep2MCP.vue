<template>
    <div>
      <v-alert type="success" variant="tonal" class="mb-4" density="compact">
        Detected Server Protocol: <strong>MCP</strong>
      </v-alert>
  
      <v-text-field
        :model-value="serverName"
        @update:model-value="$emit('update:serverName', $event)"
        label="Server Name *"
        placeholder="Enter a name for this server"
        required
        :rules="[rules.required]"
        variant="outlined"
        density="compact"
        class="mb-4"
        :disabled="isLoading"
        data-testid="step2-server-name-input"
      />
      <v-textarea
        :model-value="description"
        @update:model-value="$emit('update:description', $event)"
        label="Description (Optional)"
        rows="2"
        variant="outlined"
        density="compact"
        class="mb-4"
        :disabled="isLoading"
      />
      <v-text-field
        :model-value="websiteUrl"
        @update:model-value="$emit('update:websiteUrl', $event)"
        label="Website URL (Optional)"
        hint="e.g. https://example.com"
        :rules="[rules.simpleUrl]"
        variant="outlined"
        density="compact"
        class="mb-4"
        :disabled="isLoading"
      />
      <v-text-field
        :model-value="email"
        @update:model-value="$emit('update:email', $event)"
        label="Contact Email (Optional)"
        hint="e.g. contact@example.com"
        :rules="[rules.email]"
        variant="outlined"
        density="compact"
        class="mb-4"
        :disabled="isLoading"
      />
      <!-- Display discovered tools (read-only) -->
      <div v-if="discoveredTools && discoveredTools.length > 0">
        <h3 class="text-subtitle-1 mb-2">Discovered Tools:</h3>
        <v-chip-group>
          <v-chip v-for="tool in discoveredTools" :key="tool.name" size="small">
            {{ tool.name }}
          </v-chip>
        </v-chip-group>
      </div>
      <v-alert v-else type="info" variant="text" density="compact" class="mt-2">
        No tools discovered for this MCP server.
      </v-alert>
  
       <!-- Display Save Error Message -->
       <v-alert
         v-if="saveError && !isLoading"
         type="error"
         class="mt-4"
         density="compact"
       >
         {{ saveError }}
       </v-alert>
    </div>
  </template>
  
  <script setup lang="ts">
  import { rules } from '~/utils/validation';
  import type { ServerTool as DiscoveredTool } from '~/utils/server';
  
  // Props define the data passed from the parent and v-model bindings
  defineProps<{
    serverName: string;
    description: string;
    websiteUrl: string;
    email: string;
    isLoading: boolean;
    discoveredTools: Array<{
      name: string;
      description?: string;
      inputSchema?: {
        type: string;
        properties?: Record<string, {
          type: string;
          description?: string;
        }>;
        required?: string[];
      };
    }>;
    saveError: string; // Error specific to the save operation
  }>();
  
  // Emits define events sent back to the parent for v-model updates
  defineEmits<{
    (e: 'update:serverName', value: string): void;
    (e: 'update:description', value: string): void;
    (e: 'update:websiteUrl', value: string): void;
    (e: 'update:email', value: string): void;
    // No 'save' emit needed here, parent dialog action handles it
  }>();
  </script>
