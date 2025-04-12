<template>
  <v-dialog
    v-model="dialog"
    max-width="700px"
    persistent
  >
    <v-card>
      <v-card-title class="text-h5">
        Add New Server
      </v-card-title>

      <v-form ref="form" @submit.prevent="fetchServerInfo">
        <v-card-text>
          <!-- Step 1: Enter URL and Slug -->
          <div v-if="currentStep === 1">
            <v-text-field
              v-model="serverUrl"
              label="Server URL"
              placeholder="Enter the base URL of the server"
              hint="URL of the server you want to add (e.g., https://api.example.com or http://localhost:4001)"
              required
              :rules="[rules.required, rules.url]"
              :disabled="isLoading || isDiscovering"
              @input="autoGenerateSlug"
              class="mb-4"
            />

            <v-text-field
              v-model="slug"
              label="Server Slug"
              placeholder="my-server-slug"
              hint="Unique identifier used in URLs (e.g., my-server). Will be checked for uniqueness."
              required
              :rules="[rules.required, rules.slugFormat, slugUniqueRule]"
              :loading="isCheckingSlug"
              :error-messages="slugError"
              :disabled="isLoading || isDiscovering"
              class="mb-4"
              @update:model-value="handleSlugInput"
            />

             <!-- Fetch / Discover Button -->
             <v-btn
               color="info"
               block
               :loading="isDiscovering"
               :disabled="isLoading || !serverUrl || !slug || isCheckingSlug || !!slugError"
               @click="fetchServerInfo"
               class="mb-4"
             >
               Discover Server Type & Info
             </v-btn>

              <!-- Discovery Error Message -->
              <v-alert
                v-if="fetchError"
                type="error"
                class="mt-4"
                closable
                density="compact"
                @click:close="fetchError = ''"
              >
                {{ fetchError }}
              </v-alert>
          </div>

          <!-- Step 2: Confirm/Edit Info and Save (Only if MCP) -->
          <div v-if="currentStep === 2 && discoveredType === 'MCP'">
            <v-alert type="success" variant="tonal" class="mb-4" density="compact">
              Detected Server Type: <strong>MCP</strong>
            </v-alert>

            <v-text-field
              v-model="serverName"
              label="Server Name"
              placeholder="Server name discovered or derived from slug"
              required
              :rules="[rules.required]"
              variant="outlined"
              class="mb-4"
            />
             <v-textarea
               v-model="description"
               label="Description (Optional)"
               rows="2"
               variant="outlined"
               class="mb-4"
             />
            <v-text-field
              v-model="websiteUrl"
              label="Website URL (Optional)"
              hint="e.g. https://example.com"
              :rules="[rules.simpleUrl]"
              variant="outlined"
              class="mb-4"
            />
             <v-text-field
               v-model="email"
               label="Contact Email (Optional)"
               hint="e.g. contact@example.com"
               :rules="[rules.email]"
               variant="outlined"
               class="mb-4"
             />

             <!-- Save Button (Now in Step 2) -->
            <v-btn
              color="primary"
              block
              :loading="isLoading"
              :disabled="!serverName || !slug || discoveredType !== 'MCP'"
              @click="saveServer"
            >
              Add MCP Server
            </v-btn>

          </div>

           <!-- Step 2: Unsupported Type Message -->
           <div v-if="currentStep === 2 && discoveredType !== 'MCP' && discoveredType !== 'ERROR' && discoveredType !== 'UNKNOWN'">
             <v-alert type="warning" variant="tonal" class="mb-4">
               Detected Server Type: <strong>{{ discoveredType }}</strong><br>
               This server type is not currently supported by the Add Server feature.
             </v-alert>
           </div>

            <!-- Step 2: Unknown Type / Error Message -->
           <div v-if="currentStep === 2 && (discoveredType === 'UNKNOWN' || discoveredType === 'ERROR')">
             <v-alert type="error" variant="tonal" class="mb-4">
                Could not reliably determine the server type, or an error occurred during discovery.
                <span v-if="fetchError"><br>Details: {{ fetchError }}</span>
             </v-alert>
           </div>

        </v-card-text>

        <v-card-actions>
          <v-spacer />
          <!-- Back button only visible in Step 2 -->
          <v-btn
              v-if="currentStep === 2"
              color="grey-darken-1"
              variant="text"
              :disabled="isLoading"
              @click="currentStep = 1; fetchError = ''"
          >
             Back
          </v-btn>
          <v-btn
            color="grey-darken-1"
            variant="text"
            :disabled="isLoading || isDiscovering"
            @click="closeDialog"
          >
            Cancel
          </v-btn>
           <!-- Conditionally show Save button in Step 2 actions? No, keep it inside Step 2 div -->
        </v-card-actions>
      </v-form>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, watch, computed } from 'vue';
// Adjust import for ServerType if it's defined differently now
import type { ServerType, ServerTool } from '~/utils/server';
import { useSnackbar } from '~/composables/useSnackbar';
import { rules } from '~/utils/validation';
import { useDebounceFn } from '@vueuse/core';

// Interface definitions for Discovery Response
interface ServerInfoResponse {
    serverInfo?: { // Optional because it's MCP specific
        name: string;
        version?: string;
    };
    tools?: ServerTool[]; // Optional, MCP specific
    // Add fields for A2A and REST if needed, or keep MCPInfo separate
}

// Updated DiscoveringResponse structure
interface DiscoveringResponse {
  error?: string;
  mcp?: ServerInfoResponse;
  a2a?: { agentJsonUrl?: string }; // Example A2A info
  rest?: { openApiJsonUrl?: string; swaggerUrl?: string }; // Example REST info
}

// Props and emits
const props = defineProps({
  modelValue: {
    type: Boolean,
    default: false
  }
});

const emit = defineEmits(['update:modelValue', 'server-added']);

// Dialog state
const dialog = ref(props.modelValue);
const form = ref<HTMLFormElement | null>(null);
const currentStep = ref(1); // 1: Enter URL/Slug, 2: Confirm/Save

// Form fields
const serverUrl = ref('');
const slug = ref('');
// Removed serverType selection - it's now discovered
const serverName = ref(''); // Populated after discovery
const description = ref('');
const websiteUrl = ref('');
const email = ref('');

// Server info & Discovery State
const discoveredInfo = ref<DiscoveringResponse | null>(null);
const discoveredType = ref<ServerType | 'UNKNOWN' | 'ERROR' | null>(null);
const isLoading = ref(false); // General loading (for saving)
const isDiscovering = ref(false); // Specific loading for discovery step
const isCheckingSlug = ref(false);
const slugError = ref('');
const fetchError = ref(''); // Error during discovery or saving
const wasSlugAutoGenerated = ref(false);

const { showError, showSuccess } = useSnackbar();
const { $api, $settings, $auth } = useNuxtApp();

// Watch for dialog changes
watch(() => props.modelValue, (val) => {
  dialog.value = val;
  if (!val) {
    resetForm(); // Reset when dialog is closed
  }
});
watch(dialog, (val) => { emit('update:modelValue', val); });

// --- Slug Auto-generation & Validation (Mostly unchanged) ---
function autoGenerateSlug() {
  if (!serverUrl.value || (slug.value && !wasSlugAutoGenerated.value)) {
    if (!slug.value) { // Clear errors if URL becomes empty
      slugError.value = '';
      isCheckingSlug.value = false;
    } else if (!wasSlugAutoGenerated.value) {
       checkSlugUniquenessDebounced(); // Recheck if user modifies URL after manual slug input
    }
    return;
  }

  try {
    const url = new URL(serverUrl.value);
    let potentialSlug = url.hostname.split('.')[0] || 'server'; // Simplified generation
    slug.value = potentialSlug.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '');
    wasSlugAutoGenerated.value = true;
    checkSlugUniquenessDebounced();
  } catch (e) {
    slug.value = '';
    wasSlugAutoGenerated.value = false;
    slugError.value = '';
    isCheckingSlug.value = false;
  }
}

const checkSlugUniqueness = async () => {
  if (!slug.value || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(slug.value)) {
      slugError.value = '';
      isCheckingSlug.value = false;
      return;
  }
  isCheckingSlug.value = true;
  slugError.value = '';
  try {
    const response = await $api.getJson<{ exists: boolean }>(`/servers/check-slug/${slug.value}`);
    slugError.value = response.exists ? 'This slug is already taken.' : '';
  } catch (error) {
    console.error('Error checking slug uniqueness:', error);
    slugError.value = ''; // Allow submission, backend will catch conflict
  } finally {
    isCheckingSlug.value = false;
    form.value?.validate(); // Re-validate to show potential error
  }
};
const checkSlugUniquenessDebounced = useDebounceFn(checkSlugUniqueness, 500);
const slugUniqueRule = async () => isCheckingSlug.value ? 'Checking...' : (slugError.value || true);
function handleSlugInput() {
  wasSlugAutoGenerated.value = false;
  checkSlugUniquenessDebounced();
}
// --- End Slug Logic ---

// Reset form to initial state
function resetForm() {
  currentStep.value = 1;
  serverUrl.value = '';
  slug.value = '';
  serverName.value = '';
  description.value = '';
  websiteUrl.value = '';
  email.value = '';
  discoveredInfo.value = null;
  discoveredType.value = null;
  fetchError.value = '';
  slugError.value = '';
  isCheckingSlug.value = false;
  isDiscovering.value = false;
  isLoading.value = false;
  wasSlugAutoGenerated.value = false;
  form.value?.resetValidation();
}

// Close dialog
function closeDialog() {
  dialog.value = false; // This will trigger the watch handler to call resetForm
}

// Step 1 Action: Fetch server info & type from the discovery endpoint
async function fetchServerInfo() {
  if (!form.value) return;
  const { valid } = await form.value.validate(); // Validate URL and Slug format/uniqueness
  if (!valid || isCheckingSlug.value || !!slugError.value) return;

  isDiscovering.value = true; // Start discovery loading state
  fetchError.value = '';
  discoveredInfo.value = null;
  discoveredType.value = null;

  try {
    const gatewayAddress = $settings.get('general_gateway_address') as string;
    const discoveringHandlerPath = $settings.get('path_for_discovering_handler') as string;

    if (!discoveringHandlerPath) {
      throw new Error('Gateway discovery endpoint path is not configured.');
    }

    const effectiveGatewayAddress = gatewayAddress || window.location.origin;
    const discoveryUrl = `${effectiveGatewayAddress}${discoveringHandlerPath}`;

    // Call the *new* discovery endpoint
    const data = await $api.getJsonByRawURL<DiscoveringResponse>(discoveryUrl, {
      params: { url: serverUrl.value } // Pass target URL as query param
    });

    discoveredInfo.value = data; // Store the full response
    discoveredType.value = data.mcp ? "MCP" : data.a2a ? "A2A" : data.rest ? "REST" : 'UNKNOWN';

    if (data.error) {
        // If the discovery endpoint itself reported an error
        throw new Error(data.error);
    }

    // Populate form fields based on discovered type (only for MCP for now)
    if (discoveredType.value === 'MCP' && data.mcp?.serverInfo) {
      serverName.value = data.mcp.serverInfo.name || slug.value || 'Discovered Server'; // Use discovered name or slug
      // description, website, email are now entered by user in step 2
    } else if (discoveredType.value === 'A2A') {
        serverName.value = slug.value || 'A2A Server'; // Default name for A2A
        fetchError.value = 'Server type A2A is not yet supported for adding.'; // Show warning
    } else if (discoveredType.value === 'REST') {
        serverName.value = slug.value || 'REST API'; // Default name for REST
        fetchError.value = 'Server type REST/OpenAPI is not yet supported for adding.'; // Show warning
    } else {
        serverName.value = slug.value || 'Unknown Server';
        fetchError.value = 'Could not determine server type or type is unsupported.'; // Set error for UNKNOWN/ERROR
    }

    currentStep.value = 2; // Move to the next step

  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : 'Failed to discover server info.';
    fetchError.value = message; // Display discovery error in step 1
    showError(message);
    console.error("Error discovering server:", err);
    discoveredType.value = 'ERROR'; // Mark as error state
    currentStep.value = 2; // Move to step 2 to show the error message clearly
  } finally {
    isDiscovering.value = false; // End discovery loading state
  }
}

// Step 2 Action: Save the MCP server to the database
async function saveServer() {
  // Only proceed if it's an MCP server
  if (discoveredType.value !== 'MCP' || !discoveredInfo.value?.mcp) {
      showError("Cannot add server: Only MCP type is currently supported or discovery failed.");
      return;
  }
  // Basic validation for fields filled in Step 2
   if (!serverName.value || !slug.value) {
       showError('Server Name and Slug are required.');
       return;
   }
  if (isCheckingSlug.value || !!slugError.value) { // Re-check slug just in case
      showError('Slug is invalid or still being checked.');
      return;
  }

  isLoading.value = true; // Start general loading state for saving
  fetchError.value = '';

  try {
    if (!$auth.check()) throw new Error('You must be logged in.');
    const user = $auth.getUser();
    if (!user) throw new Error('Failed to fetch user data.');

    const mcpData = discoveredInfo.value.mcp; // MCP specific data

    // Process tools (if any were discovered)
    const processedTools = (mcpData?.tools || []).map(tool => {
      let parameters: Array<{name: string; type: string; description: string; required: boolean}> = [];
      if (tool.inputSchema && tool.inputSchema.properties) {
        parameters = Object.entries(tool.inputSchema.properties).map(([name, schema]) => {
           // Explicitly type schema as Record<string, unknown> for safer access
           const paramSchema = schema as unknown as Record<string, unknown>;
          return {
            name,
            type: paramSchema.type?.toString() || 'string', // Default type
            description: paramSchema.description?.toString() || '', // Default description
            required: tool.inputSchema?.required?.includes(name) || false
          };
        });
      }
      return {
        name: tool.name,
        description: tool.description || '',
        parameters
      };
    });

    // Prepare payload for POST request - include the DISCOVERED type
    const payload = {
      name: serverName.value,
      slug: slug.value,
      type: discoveredType.value, // Use the discovered type 'MCP'
      description: description.value || null,
      website: websiteUrl.value || null,
      email: email.value || user.email || null, // Use provided or user's email
      imageUrl: null, // Image URL not part of discovery for now
      serverUrl: serverUrl.value, // The originally entered URL
      tools: processedTools,
      // Default status/availability set by backend/prisma schema
    };

    const createdServer = await $api.postJson('/servers', payload);

    dialog.value = false; // Close the dialog first
    emit('server-added', createdServer); // Emit event
    showSuccess('MCP Server added successfully!');

    // Navigate to the new server's page using the SLUG
    if (createdServer && createdServer.slug) {
      navigateTo(`/servers/${createdServer.slug}`);
    } else {
      console.warn('Server created, but slug not found in response. Navigating to servers list.');
      navigateTo('/servers'); // Fallback navigation
    }
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : 'Failed to save server.';
    fetchError.value = message; // Show error in the dialog (Step 2)
    showError(message);
    console.error("Error saving server:", err);
  } finally {
    isLoading.value = false; // End general loading state
  }
}
</script>