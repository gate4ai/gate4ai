<template>
  <v-dialog v-model="dialog" max-width="700px" persistent scrollable>
    <v-card>
      <v-card-title class="text-h5"> Add New Server </v-card-title>
      <v-form ref="formRef" @submit.prevent="handleSubmit">
        <v-card-text style="max-height: 70vh">
          <!-- Step 1 -->
          <AddServerDialogStep1
            v-if="currentStep === 1"
            v-model:server-url="serverUrl"
            v-model:slug="slug"
            v-model:discovery-headers="discoveryHeaders"
            :is-loading="isLoading"
            :is-discovering-s-s-e="isDiscoveringSSE"
            :is-checking-slug="isCheckingSlug"
            :slug-error="slugError"
            :fetch-error="fetchError"
            :is-step1-valid="isStep1Valid"
            :slug-unique-rule="slugUniqueRule"
            :discovery-log="discoveryLogMap"
            @url-input="autoGenerateSlug"
            @slug-input="handleSlugInput"
            @discover="triggerDiscovery"
            @clear-fetch-error="clearFetchError"
          />
          <!-- Step 2 -->
          <AddServerDialogStep2MCP
            v-if="currentStep === 2 && discoveredProtocol === 'MCP'"
            v-model:server-name="serverName"
            v-model:description="description"
            v-model:website-url="websiteUrl"
            v-model:email="email"
            :is-loading="isLoading"
            :discovered-tools="discoveredInfo?.mcpTools || []"
            :save-error="saveError"
          />
          <AddServerDialogStep2A2A
            v-if="currentStep === 2 && discoveredProtocol === 'A2A'"
            v-model:server-name="serverName"
            v-model:description="description"
            v-model:website-url="websiteUrl"
            v-model:email="email"
            :is-loading="isLoading"
            :a2a-skills="discoveredInfo?.a2aSkills || []"
            :save-error="saveError"
          />
          <AddServerDialogStep2REST
            v-if="currentStep === 2 && discoveredProtocol === 'REST'"
            v-model:server-name="serverName"
            v-model:description="description"
            v-model:website-url="websiteUrl"
            v-model:email="email"
            :is-loading="isLoading"
            :protocol-version="discoveredInfo?.protocolVersion || 'Unknown'"
            :save-error="saveError"
          />
          <!-- Step 2 Status - Only show if discovery ran but found no supported protocol -->
          <div v-if="currentStep === 2 && discoveredProtocol === 'UNKNOWN'">
            <v-alert type="warning" variant="tonal" class="mb-4">
              Could not determine a supported server type (MCP, A2A, REST).
              Please check the discovery log for details or verify the server
              URL.
            </v-alert>
          </div>
          <!-- Removed the v-if for discoveredProtocol === 'ERROR' here -->
        </v-card-text>
        <v-divider />
        <v-card-actions>
          <v-spacer />
          <v-btn
            v-if="currentStep === 2"
            color="grey-darken-1"
            variant="text"
            :disabled="isLoading || isDiscoveringSSE"
            @click="goBackToStep1"
          >
            Back
          </v-btn>
          <v-btn
            color="grey-darken-1"
            variant="text"
            :disabled="isLoading || isDiscoveringSSE"
            @click="closeDialog"
          >
            Cancel
          </v-btn>
          <v-btn
            v-if="
              currentStep === 2 &&
              ['MCP', 'A2A', 'REST'].includes(discoveredProtocol || '')
            "
            id="add-server-button-step2"
            color="primary"
            variant="flat"
            :loading="isLoading"
            :disabled="isDiscoveringSSE || !isStep2Valid"
            :data-testid="`add-${discoveredProtocol?.toLowerCase()}-server-button`"
            @click="saveServer"
          >
            Add {{ discoveredProtocol }} Server
          </v-btn>
        </v-card-actions>
      </v-form>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, watch, computed, onBeforeUnmount } from "vue";
import { useDebounceFn } from "@vueuse/core";
import { useRouter } from "vue-router";
import AddServerDialogStep1 from "./AddServerDialogStep1.vue";
import AddServerDialogStep2MCP from "./AddServerDialogStep2MCP.vue";
import AddServerDialogStep2A2A from "./AddServerDialogStep2A2A.vue";
import AddServerDialogStep2REST from "./AddServerDialogStep2REST.vue";
import { useSnackbar } from "~/composables/useSnackbar";
import { useDiscovery } from "~/composables/useDiscovery"; // Import the composable
import { rules } from "~/utils/validation";
import type { ServerProtocol } from "@prisma/client";

// --- Interface Definitions ---
interface JsonSchemaProperty {
  type?: string;
  description?: string;
  properties?: Record<string, JsonSchemaProperty>; // For nested schemas
  required?: string[];
}
interface ToolParameter {
  name: string;
  type: string;
  description: string;
  required: boolean;
}
interface ProcessedTool {
  name: string;
  description: string | null;
  parameters: ToolParameter[];
}
interface ProcessedSkill {
  id: string;
  name: string;
  description: string | null;
  tags: readonly string[]; // Use readonly string[]
  examples: readonly string[]; // Use readonly string[]
  inputModes: readonly string[]; // Use readonly string[]
  outputModes: readonly string[]; // Use readonly string[]
}
interface ProcessedRestEndpoint {
  // Define if needed for REST saving logic
  path: string;
  method: string;
  description?: string | null;
  // ... other REST fields
  queryParams: {
    name: string;
    type: string;
    description?: string | null;
    required: boolean;
  }[];
  requestBody?: {
    description?: string | null;
    example?: string | null;
  } | null;
  responses: {
    statusCode: number;
    description: string;
    example?: string | null;
  }[];
}

// --- Props and Emits ---
const props = defineProps({ modelValue: { type: Boolean, default: false } });
const emit = defineEmits(["update:modelValue", "server-added"]);

// --- State ---
const dialog = ref(props.modelValue);
const formRef = ref<any>(null);
const currentStep = ref(1);
const isLoading = ref(false); // Loading state for save/slug check
const saveError = ref(""); // Error during save operation

// Server Details State
const serverUrl = ref("");
const slug = ref("");
const discoveryHeaders = ref<Record<string, string>>({});
const serverName = ref("");
const description = ref("");
const websiteUrl = ref("");
const email = ref("");

// Slug Check State
const isCheckingSlug = ref(false);
const slugError = ref("");
const wasSlugAutoGenerated = ref(false);

// --- Composables ---
const { showError, showSuccess } = useSnackbar();
const { $api, $auth } = useNuxtApp();
const router = useRouter();
const {
  isDiscoveringSSE,
  fetchError, // This now only reflects discovery *initiation* errors
  discoveryLogMap,
  discoveredInfo,
  discoveredProtocol,
  startDiscoverySSE,
  resetDiscoveryState,
} = useDiscovery();

// --- Watchers ---
watch(
  () => props.modelValue,
  (val) => {
    dialog.value = val;
    if (!val) resetForm(); // Reset when dialog is closed
  }
);
watch(dialog, (val) => emit("update:modelValue", val));

// --- Computed Properties ---
const isStep1Valid = computed(() => {
  const urlValid = rules.url(serverUrl.value) === true;
  const slugFormatValid = rules.slugFormat(slug.value) === true;
  // Ensure slugUniqueRule returns boolean true
  const slugUniqueValid = slugUniqueRule() === true;
  const headersValid = Object.keys(discoveryHeaders.value).every(
    (k) => k.trim() !== ""
  );
  return (
    serverUrl.value &&
    slug.value &&
    headersValid &&
    !slugError.value &&
    urlValid &&
    slugFormatValid &&
    slugUniqueValid && // Use the stricter check here
    !isCheckingSlug.value &&
    !isDiscoveringSSE.value // Cannot proceed if discovery is running
  );
});

const isStep2Valid = computed(() => {
  // Step 2 is valid if a *supported* protocol was found and required fields are filled
  return (
    ["MCP", "A2A", "REST"].includes(discoveredProtocol.value || "") &&
    serverName.value && // Name is required in step 2
    slug.value && // Slug still needed for saving
    !slugError.value && // Slug must still be valid
    rules.simpleUrl(websiteUrl.value) === true && // Validate optional fields
    rules.email(email.value) === true
  );
});

// --- Methods ---
function closeDialog() {
  resetDiscoveryState(); // Ensure SSE is closed if dialog is cancelled
  dialog.value = false;
}

function resetForm() {
  resetDiscoveryState(); // Reset discovery state from composable
  currentStep.value = 1;
  serverUrl.value = "";
  slug.value = "";
  discoveryHeaders.value = {};
  serverName.value = "";
  description.value = "";
  websiteUrl.value = "";
  email.value = "";
  saveError.value = "";
  slugError.value = "";
  isCheckingSlug.value = false;
  isLoading.value = false;
  wasSlugAutoGenerated.value = false;
  formRef.value?.resetValidation();
}

function goBackToStep1() {
  resetDiscoveryState(); // Clear discovery results when going back
  currentStep.value = 1;
  saveError.value = ""; // Clear save error from step 2
  // Keep serverUrl, slug, headers from step 1
}

function clearFetchError() {
  // fetchError is now readonly from the composable, no need to clear it here
  // The composable handles resetting it on new discovery attempts
}

// --- Slug Logic ---
function autoGenerateSlug() {
  slugError.value = "";
  isCheckingSlug.value = false; // Reset checking status on URL input
  if (!serverUrl.value || (slug.value && !wasSlugAutoGenerated.value)) {
    if (slug.value && !wasSlugAutoGenerated.value) {
      checkSlugUniquenessDebounced(); // Re-check if manually entered slug exists
    }
    return;
  }
  try {
    const url = new URL(serverUrl.value);
    const potentialSlug =
      url.hostname
        .toLowerCase()
        .replace(/^www\./, "")
        .replace(/[._]/g, "-")
        .replace(/[^a-z0-9-]+/g, "")
        .replace(/^-+|-+$/g, "") || "server"; // Fallback slug
    slug.value = potentialSlug;
    wasSlugAutoGenerated.value = true;
    checkSlugUniquenessDebounced(); // Check generated slug
  } catch {
    slug.value = ""; // Clear slug if URL is invalid
    wasSlugAutoGenerated.value = false;
  }
}

const checkSlugUniqueness = async () => {
  // Basic format check first
  if (!slug.value || rules.slugFormat(slug.value) !== true) {
    slugError.value = ""; // Clear error if format is invalid (rule handles message)
    isCheckingSlug.value = false;
    formRef.value?.validate(); // Trigger validation rules
    return;
  }
  isCheckingSlug.value = true;
  slugError.value = ""; // Clear previous error
  try {
    // Short delay to allow UI update for loading state
    await new Promise((resolve) => setTimeout(resolve, 50));
    const response = await $api.getJson<{ exists: boolean; error?: string }>(
      `/servers/check-slug/${slug.value}`
    );
    if (response.error) {
      slugError.value = `Could not verify slug: ${response.error}`;
    } else if (response.exists) {
      slugError.value = "This slug is already taken.";
    } else {
      slugError.value = ""; // Explicitly clear on success
    }
  } catch (error) {
    console.error("Error checking slug:", error);
    slugError.value = "Could not verify slug uniqueness."; // Generic error
  } finally {
    isCheckingSlug.value = false;
    formRef.value?.validate(); // Re-validate form after check
  }
};
const checkSlugUniquenessDebounced = useDebounceFn(checkSlugUniqueness, 350);

// This rule now directly checks the slugError ref
const slugUniqueRule = (): string | boolean => {
  if (isCheckingSlug.value) return "Checking..."; // Indicate loading state
  return slugError.value || true; // Return error message or true if valid
};

function handleSlugInput() {
  wasSlugAutoGenerated.value = false; // Manual input overrides auto-generation flag
  slugError.value = "";
  isCheckingSlug.value = false; // Reset checking status
  checkSlugUniquenessDebounced(); // Trigger debounced check on manual input
}

// --- Discovery Trigger ---
async function triggerDiscovery() {
  const validationResult = await formRef.value?.validate();
  // Check computed property which now correctly evaluates slugUniqueRule result
  if (!validationResult?.valid || !isStep1Valid.value) {
    showError("Please fix errors in Step 1 before discovering.");
    return;
  }

  isLoading.value = true; // Use general loading indicator during discovery phase
  try {
    const result = await startDiscoverySSE(
      serverUrl.value,
      discoveryHeaders.value
    );

    // Check the protocol value *after* the discovery attempt resolves/rejects
    if (discoveredProtocol.value === "ERROR") {
      // Discovery failed with a specific error reported via snackbar. Stay on Step 1.
      console.log("Discovery resulted in ERROR, staying on Step 1.");
    } else if (result) {
      // Successfully received final result (MCP, A2A, REST, or UNKNOWN)
      // Populate Step 2 fields based on result (if protocol is supported)
      if (result.protocol === "MCP") {
        serverName.value = result.name || slug.value || "MCP Server";
        description.value = result.description || "";
        websiteUrl.value = result.website || "";
      } else if (result.protocol === "A2A") {
        serverName.value = result.name || slug.value || "A2A Agent";
        description.value = result.description || "";
        websiteUrl.value = result.website || "";
      } else if (result.protocol === "REST") {
        serverName.value = result.name || slug.value || "REST API";
        description.value = result.description || "";
        websiteUrl.value = result.website || "";
      }
      // Move to Step 2 if protocol is known (MCP/A2A/REST) or UNKNOWN
      currentStep.value = 2;
    } else {
      // Discovery promise resolved with null (e.g., stream ended unexpectedly)
      // Error message handled by composable's snackbar via fetchError. Stay on Step 1.
      console.log(
        "Discovery stream ended unexpectedly or without final result, staying on Step 1."
      );
    }
  } catch (error) {
    // Catch errors from startDiscoverySSE promise rejection (fetch/setup errors)
    // Error message handled by composable's snackbar via fetchError. Stay on Step 1.
    console.error("Discovery initiation failed:", error);
  } finally {
    isLoading.value = false; // Stop general loading indicator
  }
}

// --- Save Server ---
async function saveServer() {
  if (!isStep2Valid.value) {
    showError("Please fill required fields for the server details.");
    return;
  }
  const validationResult = await formRef.value?.validate(); // Final validation
  if (!validationResult?.valid) {
    showError("Please fix validation errors before saving.");
    return;
  }

  isLoading.value = true;
  saveError.value = "";

  try {
    if (!$auth.check()) throw new Error("Not logged in.");
    const user = $auth.getUser();
    if (!user) throw new Error("User data missing.");

    // Process discovered data based on the final protocol
    let processedTools: ProcessedTool[] = [];
    let processedA2ASkills: ProcessedSkill[] = [];
    const processedRESTEndpoints: ProcessedRestEndpoint[] = [];

    if (discoveredProtocol.value === "MCP" && discoveredInfo.value?.mcpTools) {
      processedTools = discoveredInfo.value.mcpTools.map((tool) => {
        let parameters: ToolParameter[] = [];
        // Ensure inputSchema and properties exist before iterating
        if (tool.inputSchema?.properties) {
          const requiredParams = new Set(tool.inputSchema.required || []);
          parameters = Object.entries(tool.inputSchema.properties).map(
            ([paramName, paramSchema]: [string, unknown]) => {
              // Assert paramSchema is JsonSchemaProperty or provide default
              const schema = (paramSchema as JsonSchemaProperty) || {};
              return {
                name: paramName,
                type: schema.type || "string",
                description: schema.description || "",
                required: requiredParams.has(paramName),
              };
            }
          );
        }
        return {
          name: tool.name,
          description: tool.description || null, // Ensure null if empty
          parameters,
        };
      });
    } else if (
      discoveredProtocol.value === "A2A" &&
      discoveredInfo.value?.a2aSkills
    ) {
      // Direct assignment works because ProcessedSkill now accepts readonly arrays
      processedA2ASkills = discoveredInfo.value.a2aSkills.map((skill) => ({
        id: skill.id,
        name: skill.name,
        description: skill.description || null,
        tags: skill.tags || [],
        examples: skill.examples || [],
        inputModes: skill.inputModes || ["text"],
        outputModes: skill.outputModes || ["text"],
      }));
    }
    // Add processing for REST endpoints if needed

    const payload = {
      name: serverName.value,
      slug: slug.value,
      protocol: discoveredProtocol.value as ServerProtocol, // Assert type after check
      protocolVersion: discoveredInfo.value?.protocolVersion || "",
      description: description.value || null, // Send null if empty
      website: websiteUrl.value || null, // Send null if empty
      email: email.value || user.email || null, // Fallback to user email
      imageUrl: null, // Handle image upload separately
      serverUrl: serverUrl.value, // URL from step 1
      tools: processedTools,
      a2aSkills: processedA2ASkills,
      restEndpoints: processedRESTEndpoints,
      headers: {}, // Headers are managed separately
      subscriptionHeaderTemplate: [], // Template managed separately
    };

    // Use postJson for creating servers
    const createdServer = await $api.postJson<{ slug: string }>(
      "/servers",
      payload
    );

    closeDialog();
    emit("server-added", createdServer); // Emit event for parent
    showSuccess(`${payload.protocol} Server added!`);

    // Navigate to the newly created server's page
    if (createdServer?.slug) {
      router.push(`/servers/${createdServer.slug}`);
    } else {
      router.push("/servers"); // Fallback navigation
    }
  } catch (error: unknown) {
    const message =
      error instanceof Error ? error.message : "Failed to save server.";
    saveError.value = message;
    showError(`Save failed: ${message}`); // Show specific save error
    console.error("Error saving server:", error);
  } finally {
    isLoading.value = false;
  }
}

// --- Handle Submit (delegates based on step) ---
function handleSubmit() {
  if (currentStep.value === 1) {
    triggerDiscovery(); // Validation happens within triggerDiscovery
  } else if (currentStep.value === 2) {
    saveServer(); // Validation happens within saveServer
  }
}

// --- Lifecycle Hook ---
onBeforeUnmount(() => {
  resetDiscoveryState(); // Ensure cleanup on component unmount
});
</script>
