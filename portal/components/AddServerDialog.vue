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
            :is-step1-valid="isStep1Valid as boolean"
            :slug-unique-rule="slugUniqueRule"
            @url-input="autoGenerateSlug"
            @slug-input="handleSlugInput"
            @discover="fetchServerInfo"
            @clear-fetch-error="fetchError = ''"
          />

          <!-- Step 2: Confirm/Edit MCP Info -->
          <AddServerDialogStep2MCP
            v-if="currentStep === 2 && discoveredPtrotocol === 'MCP'"
            v-model:server-name="serverName"
            v-model:description="description"
            v-model:website-url="websiteUrl"
            v-model:email="email"
            :is-loading="isLoading"
            :discovered-tools="discoveredTools"
            :save-error="fetchError"
          />

          <!-- Step 2: A2A Server Details -->
          <AddServerDialogStep2A2A
            v-if="currentStep === 2 && discoveredPtrotocol === 'A2A'"
            v-model:server-name="serverName"
            v-model:description="description"
            v-model:website-url="websiteUrl"
            v-model:email="email"
            :is-loading="isLoading"
            :a2a-skills="discoveredInfo?.a2aSkills || []"
            :save-error="fetchError"
          />

          <!-- Step 2: REST Server Details -->
          <AddServerDialogStep2REST
            v-if="currentStep === 2 && discoveredPtrotocol === 'REST'"
            v-model:server-name="serverName"
            v-model:description="description"
            v-model:website-url="websiteUrl"
            v-model:email="email"
            :is-loading="isLoading"
            :protocol-version="discoveredInfo?.protocolVersion || 'Unknown'"
            :save-error="fetchError"
          />

          <!-- Step 2: Unsupported Type Message -->
          <div v-if="currentStep === 2 && discoveredPtrotocol !== 'MCP' && discoveredPtrotocol !== 'A2A' && discoveredPtrotocol !== 'REST' && discoveredPtrotocol !== 'ERROR' && discoveredPtrotocol !== 'UNKNOWN'">
            <v-alert type="warning" variant="tonal" class="mb-4">
              Detected Server Protocol: <strong>{{ discoveredPtrotocol }}</strong><br>
              This server type is not currently supported by the Add Server feature.
            </v-alert>
          </div>

          <!-- Step 2: Unknown Type / Error Message -->
          <div v-if="currentStep === 2 && (discoveredPtrotocol === 'UNKNOWN' || discoveredPtrotocol === 'ERROR')">
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
            v-if="currentStep === 2 && discoveredPtrotocol === 'MCP'"
            id="add-mcp-server-button"
            color="primary"
            variant="flat"
            :loading="isLoading"
            :disabled="isDiscovering || !isStep2Valid"
            data-testid="add-mcp-server-button"
            @click="saveServer"
          >
            Add MCP Server
          </v-btn>
          
          <!-- A2A Server button -->
          <v-btn
            v-if="currentStep === 2 && discoveredPtrotocol === 'A2A'"
            id="add-a2a-server-button"
            color="primary"
            variant="flat"
            :loading="isLoading"
            :disabled="isDiscovering || !isStep2Valid"
            data-testid="add-a2a-server-button"
            @click="saveServer"
          >
            Add A2A Server
          </v-btn>
          
          <!-- REST Server button -->
          <v-btn
            v-if="currentStep === 2 && discoveredPtrotocol === 'REST'"
            id="add-rest-server-button"
            color="primary"
            variant="flat"
            :loading="isLoading"
            :disabled="isDiscovering || !isStep2Valid"
            data-testid="add-rest-server-button"
            @click="saveServer"
          >
            Add REST Server
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
import AddServerDialogStep2A2A from './AddServerDialogStep2A2A.vue';
import AddServerDialogStep2REST from './AddServerDialogStep2REST.vue';
import { useSnackbar } from '~/composables/useSnackbar';
import { rules } from '~/utils/validation';
import type { ServerProtocol } from '@prisma/client';

// --- Interface Definitions ---
interface DiscoveredTool {
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
}

interface DiscoveringResponse {
  url: string;
  name: string;
  version: string;
  description: string;
  website: string | null;
  protocol: 'MCP' | 'A2A' | 'REST';
  protocolVersion: string;
  mcpTools?: DiscoveredTool[];
  a2aSkills?: Array<{
    id: string;
    name: string;
    description?: string;
    tags?: string[];
    examples?: string[];
    inputModes?: string[];
    outputModes?: string[];
  }>;
  restEndpoints?: Array<{
    path: string;
    method: string;
    description?: string;
    queryParams?: Array<{
      name: string;
      type: string;
      description?: string;
      required?: boolean;
    }>;
    requestBody?: {
      description?: string;
      example?: string;
    };
    responses?: Array<{
      statusCode: number;
      description: string;
      example?: string;
    }>;
  }>;
  error?: string;
}

// --- Props and Emits ---
const props = defineProps({
  modelValue: { type: Boolean, default: false }
});
const emit = defineEmits(['update:modelValue', 'server-added']);

// --- Dialog State ---
const dialog = ref(props.modelValue);
const formRef = ref<HTMLFormElement | null>(null); // Ref for the v-form
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
const discoveredPtrotocol = ref<ServerProtocol | 'UNKNOWN' | 'ERROR' | null>(null);
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
  const urlValid = rules.url(serverUrl.value) === true;
  const slugValid = rules.slugFormat(slug.value) === true;
  
  return serverUrl.value && 
         slug.value && 
         !slugError.value &&
         urlValid &&
         slugValid &&
         !isCheckingSlug.value; // Ensure check is complete
});

const isStep2Valid = computed(() => {
    // Validation specific to Step 2 (MCP)
    return (discoveredPtrotocol.value === 'MCP' || discoveredPtrotocol.value === 'A2A') &&
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
  discoveredPtrotocol.value = null;
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
        potentialSlug = potentialSlug.replace(/[._]/g, '-');
        potentialSlug = potentialSlug.replace(/[^a-z0-9-]+/g, '');
        potentialSlug = potentialSlug.replace(/^-+|-+$/g, '');
        potentialSlug = potentialSlug || 'server';

        slug.value = potentialSlug;
        wasSlugAutoGenerated.value = true;
        checkSlugUniquenessDebounced();
    } catch {
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
    discoveredPtrotocol.value = null;

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

        // Determine Type - it's now directly in the protocol field
        discoveredPtrotocol.value = data.protocol as ServerProtocol;
        
        // Handle error
        if (data.error) {
            throw new Error(`Discovery failed: ${data.error}`);
        }

        // Populate Step 2 fields based on discovery
        if (discoveredPtrotocol.value === 'MCP') {
            serverName.value = data.name || slug.value || 'MCP Server';
            description.value = data.description || '';
            websiteUrl.value = data.website || '';
            discoveredTools.value = data.mcpTools || [];
        } else if (discoveredPtrotocol.value === 'A2A') {
            serverName.value = data.name || slug.value || 'A2A Agent';
            description.value = data.description || '';
            websiteUrl.value = data.website || '';
            // Handle A2A specific data if needed
        } else if (discoveredPtrotocol.value === 'REST') {
            serverName.value = data.name || slug.value || 'REST API';
            description.value = data.description || '';
            websiteUrl.value = data.website || '';
            // Handle REST specific data if needed
        } else {
            discoveredPtrotocol.value = 'UNKNOWN';
            fetchError.value = 'Could not determine server type or type is unsupported.';
        }

        currentStep.value = 2; // Move to next step regardless of type

    } catch (error: unknown) {
        const message = error instanceof Error ? error.message : 'Failed to discover server info.';
        fetchError.value = message; // Display error in step 1
        showError(message);
        console.error("Error discovering server:", error);
        discoveredPtrotocol.value = 'ERROR';
        // Stay in Step 1 on discovery failure
    } finally {
        isDiscovering.value = false;
    }
}

// Step 2 Action: Save the MCP server
async function saveServer() {
    if (!['MCP', 'A2A', 'REST'].includes(discoveredPtrotocol.value as string)) {
        showError("Cannot add server: Only MCP, A2A, and REST types are currently supported or discovery failed.");
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
            if (tool.inputSchema && typeof tool.inputSchema === 'object' && 
                tool.inputSchema.properties && typeof tool.inputSchema.properties === 'object') {
                // Get the set of required parameter names for efficient lookup
                const requiredParams = new Set(tool.inputSchema.required || []);

                // Map over the properties (parameters) defined in the schema
                parameters = Object.entries(tool.inputSchema.properties).map(([paramName, paramSchema]) => {
                    return {
                        name: paramName,
                        type: paramSchema.type || 'string', // Default type if missing
                        description: paramSchema.description || '', // Use empty string if description is null/undefined
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

        // Process A2A skills if needed
        const processedA2ASkills = discoveredPtrotocol.value === 'A2A' && discoveredInfo.value?.a2aSkills ? 
            discoveredInfo.value.a2aSkills.map(skill => ({
                id: skill.id || skill.name.toLowerCase().replace(/\s+/g, '-'),
                name: skill.name,
                description: skill.description || '',
                tags: skill.tags || [],
                examples: skill.examples || [],
                inputModes: skill.inputModes || ['text'],
                outputModes: skill.outputModes || ['text'],
            })) : [];

        // Process REST endpoints if needed
        const processedRESTEndpoints = discoveredPtrotocol.value === 'REST' && discoveredInfo.value?.restEndpoints ? 
            discoveredInfo.value.restEndpoints.map((endpoint) => ({
                path: endpoint.path,
                method: endpoint.method || 'GET',
                description: endpoint.description || '',
                queryParams: (endpoint.queryParams || []).map((param) => ({
                    name: param.name,
                    type: param.type || 'string',
                    description: param.description || '',
                    required: param.required || false
                })),
                requestBody: endpoint.requestBody ? {
                    description: endpoint.requestBody.description || '',
                    example: endpoint.requestBody.example || ''
                } : null,
                responses: (endpoint.responses || []).map((response) => ({
                    statusCode: response.statusCode || 200,
                    description: response.description || '',
                    example: response.example || ''
                }))
            })) : [];

        const payload = {
            name: serverName.value,
            slug: slug.value,
            protocol: discoveredPtrotocol.value,
            protocolVersion: discoveredInfo.value?.protocolVersion || "",
            description: description.value || null,
            website: websiteUrl.value || null,
            email: email.value || user.email || null,
            imageUrl: null, // Not discovered
            serverUrl: serverUrl.value, // Original URL
            tools: processedTools, // MCP tools
            a2aSkills: processedA2ASkills, // A2A skills
            restEndpoints: processedRESTEndpoints // REST endpoints
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
    } catch (error: unknown) {
        const message = error instanceof Error ? error.message : 'Failed to save server.';
        fetchError.value = message; // Show save error within Step 2
        showError(message);
        console.error("Error saving server:", error);
    } finally {
        isLoading.value = false;
    }
}

// Handle form submission (e.g., pressing Enter)
function handleSubmit() {
    if (currentStep.value === 1 && isStep1Valid.value && !isCheckingSlug.value) {
        fetchServerInfo();
    } else if (currentStep.value === 2 && discoveredPtrotocol.value === 'MCP' && isStep2Valid.value) {
        saveServer();
    }
}
</script>