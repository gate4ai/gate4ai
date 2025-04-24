<template>
  <div>
    <v-text-field
      :model-value="serverUrl"
      label="Server URL *"
      placeholder="Enter the base URL of the server (e.g., https://api.example.com)"
      hint="URL to discover (e.g., https://api.example.com/mcp, http://localhost:41241, https://petstore3.swagger.io/api/v3)"
      required
      :rules="[rules.required, rules.url]"
      :disabled="isLoading || isDiscovering"
      class="mb-4"
      variant="outlined"
      density="compact"
      data-testid="add-server-url-input"
      @update:model-value="$emit('update:serverUrl', $event); $emit('url-input')"
    />

    <v-text-field
      :model-value="slug"
      label="Server Slug *"
      placeholder="my-unique-server-slug"
      hint="Unique identifier (letters, numbers, hyphens). Used in URLs."
      required
      :rules="[rules.required, rules.slugFormat, slugUniqueRule]"
      :loading="isCheckingSlug"
      :error-messages="slugError ? [slugError] : []"
      :disabled="isLoading || isDiscovering"
      class="mb-4"
      variant="outlined"
      density="compact"
      data-testid="add-server-slug-input"
      @update:model-value="$emit('update:slug', $event); $emit('slug-input')"
    />

     <v-expansion-panels class="mb-4">
       <v-expansion-panel>
         <v-expansion-panel-title>
           Discovery Headers (Optional)
         </v-expansion-panel-title>
         <v-expansion-panel-text>
             <p class="text-caption mb-2">Headers to send *only* during the discovery process (e.g., for authentication).</p>
             <KeyValueInput
               :model-value="discoveryHeaders"
               :disabled="isLoading || isDiscovering"
               @update:model-value="$emit('update:discoveryHeaders', $event)"
             />
         </v-expansion-panel-text>
       </v-expansion-panel>
     </v-expansion-panels>


    <v-btn
      id="discover-server-button"
      color="info"
      block
      :loading="isDiscovering"
      :disabled="isLoading || !isStep1Valid || isCheckingSlug || !!slugError"
      class="mb-4"
      data-testid="discover-server-button"
      @click="$emit('discover')"
    >
      Discover Server Type & Info
    </v-btn>

    <v-alert
      v-if="fetchError && !isDiscovering"
      type="error"
      class="mt-4"
      closable
      density="compact"
      @click:close="$emit('clear-fetch-error')"
    >
      {{ fetchError }}
    </v-alert>
  </div>
</template>

<script setup lang="ts">
import { rules } from '~/utils/validation';
import KeyValueInput from './KeyValueInput.vue';

// Props define the data passed from the parent and v-model bindings
defineProps<{
serverUrl: string;
slug: string;
discoveryHeaders: Record<string, string>;
isLoading: boolean;
isDiscovering: boolean;
isCheckingSlug: boolean;
slugError: string;
fetchError: string;
isStep1Valid: boolean; // Prop type is boolean
slugUniqueRule: () => string | boolean;
}>();

// Emits define events sent back to the parent
defineEmits<{
(e: 'update:serverUrl' | 'update:slug', value: string): void;
(e: 'update:discoveryHeaders', value: Record<string, string>): void;
(e: 'url-input' | 'slug-input' | 'discover' | 'clear-fetch-error'): void;
}>();
</script>