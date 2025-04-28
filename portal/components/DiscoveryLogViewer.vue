<template>
  <div
    data-testid="discovery-log-viewer"
    class="discovery-log-container pa-2 border rounded"
  >
    <p class="text-subtitle-2 mb-2">Discovery Log:</p>
    <div class="log-entries-wrapper">
      <div v-if="sortedLogEntries.length === 0" class="text-grey">
        Waiting for discovery logs...
      </div>
      <div
        v-for="entry in sortedLogEntries"
        :key="entry.stepId + '-' + entry.timestamp"
        class="log-entry"
        :data-testid="`log-entry-${entry.protocol}-${
          entry.method
        }-${entry.step.replace(/\s+/g, '-')}`"
      >
        <v-icon
          :icon="getStatusIcon(entry.status)"
          :color="getStatusColor(entry.status)"
          size="x-small"
          class="mr-1"
        />
        <span class="text-caption font-weight-medium mr-1"
          >[{{ entry.protocol }} - {{ entry.method }}]</span
        >
        <span class="text-caption mr-1">{{ entry.step }}</span>
        <span v-if="entry.url" class="text-caption text-grey-darken-1 mr-1"
          >({{ entry.url }})</span
        >
        <v-tooltip v-if="entry.details" location="bottom" max-width="400px">
          <template #activator="{ props: tooltipProps }">
            <span
              class="text-caption font-italic"
              :class="`text-${getStatusColor(entry.status)}`"
              v-bind="tooltipProps"
              >({{ entry.details.message || entry.status }})</span
            >
          </template>
          <div>
            <strong>Status:</strong> {{ entry.status }}<br >
            <strong v-if="entry.details.type"
              >Type: {{ entry.details.type }}</strong
            ><br v-if="entry.details.type" >
            <strong v-if="entry.details.statusCode !== undefined"
              >Status Code: {{ entry.details.statusCode }}</strong
            ><br v-if="entry.details.statusCode !== undefined" >
            <strong>Message:</strong> {{ entry.details.message }}<br >
            <div v-if="entry.details.responseBodyPreview">
              <strong>Preview:</strong>
              <pre class="log-preview">{{
                entry.details.responseBodyPreview
              }}</pre>
            </div>
          </div>
        </v-tooltip>
        <span
          v-else
          class="text-caption font-italic"
          :class="`text-${getStatusColor(entry.status)}`"
        >
          ({{ entry.status }})
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";

// Interface matching backend structure
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

// Accept the Map as a prop
const props = defineProps<{
  log: Map<string, DiscoveryLogEntry>;
}>();

// Compute a sorted array from the Map's values for rendering
const sortedLogEntries = computed(() => {
  return Array.from(props.log.values()).sort((a, b) => {
    // Basic timestamp comparison, assumes consistent format
    return a.timestamp.localeCompare(b.timestamp);
  });
});

function getStatusIcon(status: string): string {
  switch (status) {
    case "attempting":
      return "mdi-timer-sand";
    case "success":
      return "mdi-check-circle";
    case "error":
      return "mdi-close-circle";
    default:
      return "mdi-help-circle";
  }
}

function getStatusColor(status: string): string {
  switch (status) {
    case "attempting":
      return "info";
    case "success":
      return "success";
    case "error":
      return "error";
    default:
      return "grey";
  }
}
</script>

<style scoped>
.discovery-log-container {
  background-color: #f5f5f5; /* Light grey background */
  max-height: 250px; /* Limit height */
  overflow-y: auto; /* Enable vertical scroll */
  font-family: monospace; /* Use monospace font for logs */
}
.log-entries-wrapper {
  display: flex;
  flex-direction: column;
}
.log-entry {
  margin-bottom: 4px;
  line-height: 1.3;
  white-space: normal; /* Allow wrapping */
  word-break: break-all; /* Break long URLs */
}
.log-preview {
  white-space: pre-wrap; /* Wrap preview text */
  word-break: break-all;
  background-color: #e0e0e0;
  padding: 2px 4px;
  border-radius: 3px;
  margin-top: 2px;
  font-size: 0.75rem;
}
.v-tooltip > .v-overlay__content {
  background: rgba(97, 97, 97, 0.9); /* Vuetify grey-darken-1 */
  color: white;
  border-radius: 4px;
  padding: 8px;
  font-size: 12px;
}
</style>
