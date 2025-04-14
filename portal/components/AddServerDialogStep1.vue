<template>
    <div>
      <v-text-field
        :model-value="serverUrl"
        @update:model-value="$emit('update:serverUrl', $event); $emit('url-input')"
        label="Server URL *"
        placeholder="Enter the base URL of the server"
        hint="URL of the server (e.g., https://api.example.com/mcp or http://localhost:4001/sse?key=...)"
        required
        :rules="[rules.required, rules.url]"
        :disabled="isLoading || isDiscovering"
        class="mb-4"
        variant="outlined"
        density="compact"
        data-testid="add-server-url-input"
      />
  
      <v-text-field
        :model-value="slug"
        @update:model-value="$emit('update:slug', $event); $emit('slug-input')"
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
      />
  
      <!-- Fetch / Discover Button -->
      <v-btn
        color="info"
        block
        :loading="isDiscovering"
        :disabled="isLoading || !isStep1Valid || isCheckingSlug || !!slugError"
        @click="$emit('discover')"
        class="mb-4"
        id="discover-server-button"
        data-testid="discover-server-button"
      >
        Discover Server Type & Info
      </v-btn>
  
      <!-- Discovery Error Message -->
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
  
  // Props define the data passed from the parent and v-model bindings
  defineProps<{
    serverUrl: string;
    slug: string;
    isLoading: boolean;
    isDiscovering: boolean;
    isCheckingSlug: boolean;
    slugError: string;
    fetchError: string;
    isStep1Valid: boolean;
    slugUniqueRule: () => boolean | string; // Pass the rule function from parent
  }>();
  
  // Emits define events sent back to the parent
  defineEmits<{
    (e: 'update:serverUrl', value: string): void;
    (e: 'update:slug', value: string): void;
    (e: 'url-input'): void; // Notify parent about URL input for auto-slug generation
    (e: 'slug-input'): void; // Notify parent about manual slug input
    (e: 'discover'): void;
    (e: 'clear-fetch-error'): void;
  }>();
  </script>
  