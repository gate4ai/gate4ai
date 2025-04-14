<template>
  <v-dialog
    v-model="dialog"
    max-width="700px"
    persistent
    scrollable
  >
    <v-card>
      <v-card-title class="text-h5">
        Add New Server
      </v-card-title>

      <v-form ref="formRef" @submit.prevent="handleSubmit">
        <v-card-text style="max-height: 70vh;">
          <!-- Step 1: Enter URL, Slug, Discover -->
          <AddServerDialogStep1
            v-if="currentStep === 1"
            v-model:server-url="serverUrl"
            v-model:slug="slug"
            :is-loading="isLoading"
            :is-discovering="isDiscovering"
            :is-checking-slug="isCheckingSlug"
            :slug-error="slugError"
            :fetch-error="fetchError"
            :is-step1-valid="isStep1Valid"
            :slug-unique-rule="slugUniqueRule"
            @url-input="autoGenerateSlug"
            @slug-input="handleSlugInput"
            @discover="fetchServerInfo"
            @clear-fetch-error="fetchError = ''"
          />

          <!-- Step 2: Confirm/Edit MCP Info -->
          <AddServerDialogStep2MCP
            v-if="currentStep === 2 && discoveredType === 'MCP'"
            v-model:server-name="serverName"
            v-model:description="description"
            v-model:website-url="websiteUrl"
            v-model:email="email"
            :is-loading="isLoading"
            :discovered-tools="discoveredTools"
            :save-error="fetchError"
          />

          <!-- Step 2: Unsupported Type Message -->
          <div v-if="currentStep === 2 && discoveredType !== 'MCP' && discoveredType !== 'ERROR' && discoveredType !== 'UNKNOWN'">
            <v-alert type="warning" variant="tonal" class="mb-4">
              Detected Server Type: <strong>{{ discoveredType }}</strong><br>
              This server type is not currently supported by the Add Server feature. Automatic addition is only available for MCP servers.
            </v-alert>
          </div>

          <!-- Step 2: Unknown Type / Error Message -->
          <div v-if="currentStep === 2 && (discoveredType === 'UNKNOWN' || discoveredType === 'ERROR')">
            <v-alert type="error" variant="tonal" class="mb-4">
              Could not reliably determine the server type, or an error occurred during discovery. Please check the URL and server status.
              <span v-if="fetchError"><br>Details: {{ fetchError }}</span>
            </v-alert>
          </div>

        </v-card-text>

        <v-divider />

        <v-card-actions>
          <v-spacer />
          <!-- Back button only visible in Step 2 -->
          <v-btn
            v-if="currentStep === 2"
            color="grey-darken-1"
            variant="text"
            :disabled="isLoading"
            @click="goBackToStep1"
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
          <!-- Save button only visible in Step 2 and only for MCP type -->
          <v-btn
            v-if="currentStep === 2 && discoveredType === 'MCP'"
            color="primary"
            variant="flat"
            :loading="isLoading"
            :disabled="isDiscovering || !isStep2Valid"
            @click="saveServer"
            id="add-mcp-server-button"
            data-testid="add-mcp-server-button"
          >
            Add MCP Server
          </v-btn>
        </v-card-actions>
      </v-form>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, watch, computed } from 'vue';
import { useDebounceFn } from '@vueuse/core';
import { useRouter } from 'vue-router';
import AddServerDialogStep1 from './AddServerDialogStep1.vue';
import AddServerDialogStep2MCP from './AddServerDialogStep2MCP.vue';
import type { ServerType, ServerTool as DiscoveredTool } from '~/utils/server';
import { useSnackbar } from '~/composables/useSnackbar';
import { rules } from '~/utils/validation';

// --- Interface Definitions ---
interface MCPInfoResponse {
    serverInfo?: { name: string; version?: string };
    tools?: DiscoveredTool[];
}
interface A2AInfoResponse { agentJsonUrl?: string }
interface RESTInfoResponse { openApiJsonUrl?: string; swaggerUrl?: string }
interface DiscoveringResponse {
  error?: string;
  mcp?: MCPInfoResponse;
  a2a?: A2AInfoResponse;
  rest?: RESTInfoResponse;
}

// --- Props and Emits ---
const props = defineProps({
  modelValue: { type: Boolean, default: false }
});
const emit = defineEmits(['update:modelValue', 'server-added']);

// --- Dialog State ---
const dialog = ref(props.modelValue);
const formRef = ref<any>(null); // Ref for the v-form
const currentStep = ref(1); // 1: Enter URL/Slug, 2: Confirm/Save

// --- Form Data State (Managed by Parent) ---
const serverUrl = ref('');
const slug = ref('');
const serverName = ref(''); // Populated after discovery (for Step 2)
const description = ref(''); // (for Step 2)
const websiteUrl = ref(''); // (for Step 2)
const email = ref(''); // (for Step 2)

// --- Discovery & Server State ---
const discoveredInfo = ref<DiscoveringResponse | null>(null);
const discoveredTools = ref<DiscoveredTool[]>([]);
const discoveredType = ref<ServerType | 'UNKNOWN' | 'ERROR' | null>(null);
const fetchError = ref(''); // Covers discovery and save errors
const wasSlugAutoGenerated = ref(false);

// --- Loading States ---
const isLoading = ref(false); // General loading (saving)
const isDiscovering = ref(false); // Specific loading for discovery step
const isCheckingSlug = ref(false);
const slugError = ref('');

// --- Composables & Plugins ---
const { showError, showSuccess } = useSnackbar();
const { $api, $settings, $auth } = useNuxtApp();
const router = useRouter();

// --- Watchers ---
watch(() => props.modelValue, (val) => {
  dialog.value = val;
  if (!val) resetForm();
});
watch(dialog, (val) => emit('update:modelValue', val));

// --- Computed Properties ---
const isStep1Valid = computed(() => {
  return serverUrl.value && slug.value && !slugError.value &&
         rules.url(serverUrl.value) === true &&
         rules.slugFormat(slug.value) === true &&
         !isCheckingSlug.value; // Ensure check is complete
});

const isStep2Valid = computed(() => {
    // Validation specific to Step 2 (MCP)
    return discoveredType.value === 'MCP' &&
           serverName.value && // Name is required
           slug.value && // Slug must still be valid
           !slugError.value && // No slug errors
           !isCheckingSlug.value && // Slug check complete
           // Add checks for other required fields in step 2 if any
           rules.simpleUrl(websiteUrl.value) === true && // Validate optional fields
           rules.email(email.value) === true;
});

// --- Methods ---
function closeDialog() {
  dialog.value = false; // Watcher will trigger resetForm
}

function resetForm() {
  currentStep.value = 1;
  serverUrl.value = '';
  slug.value = '';
  serverName.value = '';
  description.value = '';
  websiteUrl.value = '';
  email.value = '';
  discoveredInfo.value = null;
  discoveredTools.value = [];
  discoveredType.value = null;
  fetchError.value = '';
  slugError.value = '';
  isCheckingSlug.value = false;
  isDiscovering.value = false;
  isLoading.value = false;
  wasSlugAutoGenerated.value = false;
  formRef.value?.resetValidation();
}

function goBackToStep1() {
    currentStep.value = 1;
    fetchError.value = ''; // Clear errors when going back
    // Don't reset URL/Slug, allow user to modify them
}

// --- Slug Auto-generation & Validation Logic ---
function autoGenerateSlug() {
    // Reset error/check state when URL changes
    slugError.value = '';
    isCheckingSlug.value = false;

    if (!serverUrl.value || (slug.value && !wasSlugAutoGenerated.value)) {
        if (slug.value && !wasSlugAutoGenerated.value) {
            checkSlugUniquenessDebounced(); // Recheck if user modifies URL after manual slug input
        }
        return;
    }

    try {
        const url = new URL(serverUrl.value);
        let potentialSlug = url.hostname.toLowerCase().replace(/^www\./, '');
        potentialSlug = potentialSlug.replace(/[\._]/g, '-');
        potentialSlug = potentialSlug.replace(/[^a-z0-9-]+/g, '');
        potentialSlug = potentialSlug.replace(/^-+|-+$/g, '');
        potentialSlug = potentialSlug || 'server';

        slug.value = potentialSlug;
        wasSlugAutoGenerated.value = true;
        checkSlugUniquenessDebounced();
    } catch (e) {
        slug.value = ''; // Clear slug if URL is invalid
        wasSlugAutoGenerated.value = false;
    }
}

const checkSlugUniqueness = async () => {
  // Skip check if slug is empty or format is invalid (let rule handle it)
  if (!slug.value || rules.slugFormat(slug.value) !== true) {
      slugError.value = ''; // Clear our async error
      isCheckingSlug.value = false;
      formRef.value?.validate(); // Re-validate form
      return;
  }

  isCheckingSlug.value = true;
  slugError.value = ''; // Clear previous error

  try {
    // Add a small delay to allow UI to update with loading state
    await new Promise(resolve => setTimeout(resolve, 50));
    const response = await $api.getJson<{ exists: boolean }>(`/servers/check-slug/${slug.value}`);
    slugError.value = response.exists ? 'This slug is already taken.' : '';
  } catch (error) {
    console.error('Error checking slug uniqueness:', error);
    slugError.value = 'Could not verify slug uniqueness.'; // Indicate check failure
  } finally {
    isCheckingSlug.value = false;
    formRef.value?.validate(); // Re-validate to show error or clear 'Checking...'
  }
};

const checkSlugUniquenessDebounced = useDebounceFn(checkSlugUniqueness, 350);

// Rule function passed to the child component
const slugUniqueRule = () => {
    if (isCheckingSlug.value) return 'Checking...';
    return slugError.value || true; // Return error message or true if valid
};

function handleSlugInput() {
  wasSlugAutoGenerated.value = false; // User is typing manually
  slugError.value = ''; // Clear previous async error on input
  isCheckingSlug.value = false; // Cancel any pending check visually
  checkSlugUniquenessDebounced(); // Start debounced check
}
// --- End Slug Logic ---

// Step 1 Action: Fetch server info & type
async function fetchServerInfo() {
    // Validate Step 1 form fields within the parent form context
    const validationResult = await formRef.value?.validate();
    if (!validationResult?.valid || isCheckingSlug.value || !!slugError.value) {
        showError("Please fix the errors in the form before discovering.");
        return;
    }

    isDiscovering.value = true;
    fetchError.value = '';
    discoveredInfo.value = null;
    discoveredTools.value = [];
    discoveredType.value = null;

    try {
        const gatewayAddress = $settings.get('general_gateway_address') as string;
        const discoveringHandlerPath = $settings.get('path_for_discovering_handler') as string;
        if (!discoveringHandlerPath) throw new Error('Gateway discovery endpoint path is not configured.');

        const effectiveGatewayAddress = gatewayAddress || window.location.origin;
        const discoveryUrlPath = discoveringHandlerPath.startsWith('/') ? discoveringHandlerPath : `/${discoveringHandlerPath}`;
        const discoveryUrl = `${effectiveGatewayAddress}${discoveryUrlPath}`;

        console.log(`Attempting discovery at: ${discoveryUrl} with target: ${serverUrl.value}`);
        const data = await $api.getJsonByRawURL<DiscoveringResponse>(discoveryUrl, {
            params: { url: serverUrl.value }
        });
        console.log('Discovery response:', data);
        discoveredInfo.value = data;

        // Determine Type
        if (data.mcp) discoveredType.value = 'MCP';
        else if (data.a2a) discoveredType.value = 'A2A';
        else if (data.rest) discoveredType.value = 'REST';
        else discoveredType.value = 'UNKNOWN';

        if (data.error && discoveredType.value === 'UNKNOWN') {
            throw new Error(`Discovery failed: ${data.error}`);
        }

        // Populate Step 2 fields based on discovery
        if (discoveredType.value === 'MCP' && data.mcp) {
            serverName.value = data.mcp.serverInfo?.name || slug.value || 'MCP Server';
            discoveredTools.value = data.mcp.tools || [];
        } else {
             // Set default names for other types or if MCP discovery failed
             serverName.value = slug.value || `${discoveredType.value || 'Unknown'} Server`;
             if (discoveredType.value !== 'MCP') {
                 // Set informative message for non-MCP types to be shown in Step 2
                 // fetchError.value = `Server type ${discoveredType.value} is not currently supported for automatic addition.`;
             } else if (discoveredType.value === 'UNKNOWN' || discoveredType.value === 'ERROR') {
                 fetchError.value = 'Could not determine server type or type is unsupported.';
             }
        }

        currentStep.value = 2; // Move to next step

    } catch (err: unknown) {
        const message = err instanceof Error ? err.message : 'Failed to discover server info.';
        fetchError.value = message; // Display error in step 1
        showError(message);
        console.error("Error discovering server:", err);
        discoveredType.value = 'ERROR';
        // Stay in Step 1 on discovery failure
    } finally {
        isDiscovering.value = false;
    }
}

// Step 2 Action: Save the MCP server
async function saveServer() {
    if (discoveredType.value !== 'MCP' || !discoveredInfo.value?.mcp) {
        showError("Cannot add server: Only MCP type is currently supported or discovery failed.");
        return;
    }

    // Re-validate the whole form before submitting from Step 2
    const validationResult = await formRef.value?.validate();
    if (!validationResult?.valid || !isStep2Valid.value) { // Double check Step 2 specific validation
        showError("Please fix the errors in the form before saving.");
        return;
    }
    // Final slug check
    if (isCheckingSlug.value || !!slugError.value) {
      showError('Slug is invalid or still being checked.');
      return;
    }


    isLoading.value = true;
    fetchError.value = ''; // Clear previous errors before saving

    try {
        if (!$auth.check()) throw new Error('You must be logged in.');
        const user = $auth.getUser();
        if (!user) throw new Error('Failed to fetch user data.');

        const processedTools = (discoveredTools.value || []).map(tool => {
            let parameters: { name: string; type: string; description: string; required: boolean }[] = [];

            // Check if inputSchema and properties exist
            if (tool.inputSchema && typeof tool.inputSchema === 'object' && tool.inputSchema.properties && typeof tool.inputSchema.properties === 'object') {
                // Get the set of required parameter names for efficient lookup
                const requiredParams = new Set(tool.inputSchema.required || []);

                // Map over the properties (parameters) defined in the schema
                parameters = Object.entries(tool.inputSchema.properties).map(([paramName, paramSchema]) => {
                    // Ensure paramSchema is an object before accessing properties
                    const schemaObj = typeof paramSchema === 'object' && paramSchema !== null ? paramSchema : {};

                    const description = typeof schemaObj.description === 'string' ? schemaObj.description : '';
                    const type = typeof schemaObj.type === 'string' ? schemaObj.type : 'string'; // Default type if missing

                    return {
                        name: paramName,
                        type: type,
                        description: description,
                        required: requiredParams.has(paramName) // Check if name is in the required set
                    };
                });
            }

            return {
                name: tool.name,
                description: tool.description || '', // Use empty string if description is null/undefined
                parameters: parameters // Assign the processed parameters array
            };
        });

        const payload = {
            name: serverName.value,
            slug: slug.value,
            type: discoveredType.value, // 'MCP'
            description: description.value || null,
            website: websiteUrl.value || null,
            email: email.value || user.email || null,
            imageUrl: null, // Not discovered
            serverUrl: serverUrl.value, // Original URL
            tools: processedTools, // Use the correctly processed tools
        };

        console.log("Saving server with payload:", payload);
        const createdServer = await $api.postJson<{ slug: string }>('/servers', payload);

        dialog.value = false; // Close dialog first
        emit('server-added', createdServer);
        showSuccess('MCP Server added successfully!');

        if (createdServer && createdServer.slug) {
            router.push(`/servers/${createdServer.slug}`);
        } else {
            router.push('/servers'); // Fallback
        }
    } catch (err: unknown) {
        const message = err instanceof Error ? err.message : 'Failed to save server.';
        fetchError.value = message; // Show save error within Step 2
        showError(message);
        console.error("Error saving server:", err);
    } finally {
        isLoading.value = false;
    }
}

// Handle form submission (e.g., pressing Enter)
function handleSubmit() {
    if (currentStep.value === 1 && isStep1Valid.value && !isCheckingSlug.value) {
        fetchServerInfo();
    } else if (currentStep.value === 2 && discoveredType.value === 'MCP' && isStep2Valid.value) {
        saveServer();
    }
}
</script>