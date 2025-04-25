<template>
  <div>
    <v-alert type="info" variant="tonal" class="mb-4" density="compact">
      Detected Server Protocol: <strong>REST</strong>
    </v-alert>

    <v-text-field
      :model-value="serverName"
      label="Server Name *"
      placeholder="Enter a name for this server"
      required
      :rules="[rules.required]"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      data-testid="step2-server-name-input"
      @update:model-value="$emit('update:serverName', $event)"
    />
    <v-textarea
      :model-value="description"
      label="Description (Optional)"
      rows="2"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      @update:model-value="$emit('update:description', $event)"
    />
    <v-text-field
      :model-value="websiteUrl"
      label="Website URL (Optional)"
      hint="e.g. https://example.com"
      :rules="[rules.simpleUrl]"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      @update:model-value="$emit('update:websiteUrl', $event)"
    />
    <v-text-field
      :model-value="email"
      label="Contact Email (Optional)"
      hint="e.g. contact@example.com"
      :rules="[rules.email]"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      @update:model-value="$emit('update:email', $event)"
    />
    <v-alert type="info" variant="text" density="compact" class="mt-2">
      This appears to be a REST API or OpenAPI service. 
      Version: {{ protocolVersion }}
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

// Props define the data passed from the parent and v-model bindings
defineProps<{
  serverName: string;
  description: string;
  websiteUrl: string;
  email: string;
  isLoading: boolean;
  protocolVersion: string;
  saveError: string; // Error specific to the save operation
}>();

// Emits define events sent back to the parent for v-model updates
defineEmits<{
  (e: 'update:serverName' | 'update:description' | 'update:websiteUrl' | 'update:email', value: string): void;
  // No 'save' emit needed here, parent dialog action handles it
}>();
</script> 