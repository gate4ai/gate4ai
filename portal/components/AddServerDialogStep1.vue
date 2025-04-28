<template>
  <div>
    <v-text-field
      :model-value="serverUrl"
      label="Server URL *"
      placeholder="Enter the base URL of the server (e.g., https://api.example.com)"
      hint="URL to discover (e.g., https://api.example.com/mcp, http://localhost:41241, https://petstore3.swagger.io/api/v3)"
      required
      :rules="[rules.required, rules.url]"
      :disabled="isLoading || isDiscoveringSSE"
      class="mb-4"
      variant="outlined"
      density="compact"
      data-testid="add-server-url-input"
      @update:model-value="
        $emit('update:serverUrl', $event);
        $emit('url-input');
      "
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
      :disabled="isLoading || isDiscoveringSSE"
      class="mb-4"
      variant="outlined"
      density="compact"
      data-testid="add-server-slug-input"
      @update:model-value="
        $emit('update:slug', $event);
        $emit('slug-input');
      "
    />

    <v-expansion-panels class="mb-4">
      <v-expansion-panel :disabled="isLoading || isDiscoveringSSE">
        <v-expansion-panel-title>
          Discovery Headers (Optional)
        </v-expansion-panel-title>
        <v-expansion-panel-text>
          <p class="text-caption mb-2">
            Headers to send *only* during the discovery process (e.g., for
            authentication). These are NOT saved with the server.
          </p>
          <KeyValueInput
            :model-value="discoveryHeaders"
            :disabled="isLoading || isDiscoveringSSE"
            @update:model-value="$emit('update:discoveryHeaders', $event)"
          />
        </v-expansion-panel-text>
      </v-expansion-panel>
    </v-expansion-panels>

    <v-btn
      id="discover-server-button"
      color="info"
      block
      :loading="isDiscoveringSSE"
      :disabled="isLoading || !isStep1Valid || isCheckingSlug || !!slugError"
      class="mb-4"
      data-testid="discover-server-button"
      @click="$emit('discover')"
    >
      Discover Server Type & Info
    </v-btn>

    <!-- Discovery Log Viewer -->
    <DiscoveryLogViewer
      v-if="isDiscoveringSSE || discoveryLog.size > 0"
      :log="discoveryLog"
      class="mb-4"
    />

    <v-alert
      v-if="fetchError && !isDiscoveringSSE"
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
import { rules } from "~/utils/validation";
import KeyValueInput from "./KeyValueInput.vue";
import DiscoveryLogViewer from "./DiscoveryLogViewer.vue";

// Interface matching backend structure for logs
interface LogDetails {
  type?: string;
  message?: string;
  statusCode?: number;
  responseBodyPreview?: string;
}
interface DiscoveryLogEntry {
  stepId: string;
  timestamp: string;
  protocol: string;
  method: string;
  step: string;
  url?: string;
  status: "attempting" | "success" | "error";
  details?: LogDetails;
}

// Props define the data passed from the parent and v-model bindings
defineProps<{
  serverUrl: string;
  slug: string;
  discoveryHeaders: Record<string, string>;
  isLoading: boolean; // General loading (e.g., slug check)
  isDiscoveringSSE: boolean; // Specific discovery loading state
  isCheckingSlug: boolean;
  slugError: string;
  fetchError: string; // Discovery setup/fetch error
  isStep1Valid: boolean;
  slugUniqueRule: () => string | boolean;
  discoveryLog: Map<string, DiscoveryLogEntry>;
}>();

// Emits define events sent back to the parent
defineEmits<{
  (e: "update:serverUrl" | "update:slug", value: string): void;
  (e: "update:discoveryHeaders", value: Record<string, string>): void;
  (e: "url-input" | "slug-input" | "discover" | "clear-fetch-error"): void;
}>();
</script>
