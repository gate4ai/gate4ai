<template>
  <v-dialog
    v-model="dialog"
    max-width="700px"
    persistent
    scrollable
  >
    <v-card>
      <v-card-title class="text-h5"> Add New Server </v-card-title>
      <v-form ref="formRef" @submit.prevent="handleSubmit">
        <v-card-text style="max-height: 70vh;">
          <!-- Step 1 -->
          <AddServerDialogStep1
            v-if="currentStep === 1"
            v-model:server-url="serverUrl"
            v-model:slug="slug"
            v-model:discovery-headers="discoveryHeaders"
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
          <!-- Step 2 -->
          <AddServerDialogStep2MCP v-if="currentStep === 2 && discoveredPtrotocol === 'MCP'" v-model:server-name="serverName" v-model:description="description" v-model:website-url="websiteUrl" v-model:email="email" :is-loading="isLoading" :discovered-tools="discoveredTools" :save-error="fetchError" />
          <AddServerDialogStep2A2A v-if="currentStep === 2 && discoveredPtrotocol === 'A2A'" v-model:server-name="serverName" v-model:description="description" v-model:website-url="websiteUrl" v-model:email="email" :is-loading="isLoading" :a2a-skills="discoveredInfo?.a2aSkills || []" :save-error="fetchError" />
          <AddServerDialogStep2REST v-if="currentStep === 2 && discoveredPtrotocol === 'REST'" v-model:server-name="serverName" v-model:description="description" v-model:website-url="websiteUrl" v-model:email="email" :is-loading="isLoading" :protocol-version="discoveredInfo?.protocolVersion || 'Unknown'" :save-error="fetchError" />
          <!-- Step 2 Status -->
          <div v-if="currentStep === 2 && !['MCP', 'A2A', 'REST', 'ERROR', 'UNKNOWN'].includes(discoveredPtrotocol || '')"> <v-alert type="warning" variant="tonal" class="mb-4"> Detected: <strong>{{ discoveredPtrotocol }}</strong><br>Unsupported type.</v-alert> </div>
          <div v-if="currentStep === 2 && (discoveredPtrotocol === 'UNKNOWN' || discoveredPtrotocol === 'ERROR')"> <v-alert type="error" variant="tonal" class="mb-4"> Could not determine type or discovery error.<span v-if="fetchError"><br>Details: {{ fetchError }}</span> </v-alert> </div>
        </v-card-text>
        <v-divider />
        <v-card-actions>
          <v-spacer />
          <v-btn v-if="currentStep === 2" color="grey-darken-1" variant="text" :disabled="isLoading" @click="goBackToStep1"> Back </v-btn>
          <v-btn color="grey-darken-1" variant="text" :disabled="isLoading || isDiscovering" @click="closeDialog"> Cancel </v-btn>
          <v-btn v-if="currentStep === 2 && ['MCP', 'A2A', 'REST'].includes(discoveredPtrotocol || '')" id="add-server-button-step2" color="primary" variant="flat" :loading="isLoading" :disabled="isDiscovering || !isStep2Valid" :data-testid="`add-${discoveredPtrotocol?.toLowerCase()}-server-button`" @click="saveServer"> Add {{ discoveredPtrotocol }} Server </v-btn>
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
// NEW: Interface for JSON Schema property (simplified)
interface JsonSchemaProperty {
    type?: string;
    description?: string;
    // Add other fields if needed by UI or processing logic
}

// Use the new interface for Tool parameters
interface DiscoveredTool {
  name: string;
  description?: string;
  inputSchema?: {
    type?: string;
    properties?: Record<string, JsonSchemaProperty>; // Use defined interface
    required?: string[];
  };
}

// Other interfaces remain the same
interface DiscoveringResponse { url: string; name: string; version: string; description: string; website: string | null; protocol: 'MCP' | 'A2A' | 'REST'; protocolVersion: string; mcpTools?: DiscoveredTool[]; a2aSkills?: Array<any>; restEndpoints?: Array<any>; error?: string; }
const props = defineProps({ modelValue: { type: Boolean, default: false } });
const emit = defineEmits(['update:modelValue', 'server-added']);
const dialog = ref(props.modelValue);
const formRef = ref<HTMLFormElement | null>(null);
const currentStep = ref(1);
const serverUrl = ref('');
const slug = ref('');
const discoveryHeaders = ref<Record<string, string>>({});
const serverName = ref('');
const description = ref('');
const websiteUrl = ref('');
const email = ref('');
const discoveredInfo = ref<DiscoveringResponse | null>(null);
const discoveredTools = ref<DiscoveredTool[]>([]);
const discoveredPtrotocol = ref<ServerProtocol | 'UNKNOWN' | 'ERROR' | null>(null);
const fetchError = ref('');
const wasSlugAutoGenerated = ref(false);
const isLoading = ref(false);
const isDiscovering = ref(false);
const isCheckingSlug = ref(false);
const slugError = ref('');
const { showError, showSuccess } = useSnackbar();
const { $api, $settings, $auth } = useNuxtApp();
const router = useRouter();

// --- Watchers remain the same ---
watch(() => props.modelValue, (val) => { dialog.value = val; if (!val) resetForm(); });
watch(dialog, (val) => emit('update:modelValue', val));

// --- Computed Properties remain the same ---
const isStep1Valid = computed(() => { const urlValid = rules.url(serverUrl.value) === true; const slugValid = rules.slugFormat(slug.value) === true; const headersValid = Object.keys(discoveryHeaders.value).every(k => k.trim() !== ''); return serverUrl.value && slug.value && headersValid && !slugError.value && urlValid && slugValid && !isCheckingSlug.value; });
const isStep2Valid = computed(() => { return (['MCP', 'A2A', 'REST'].includes(discoveredPtrotocol.value || '')) && serverName.value && slug.value && !slugError.value && !isCheckingSlug.value && rules.simpleUrl(websiteUrl.value) === true && rules.email(email.value) === true; });

// --- Methods ---
function closeDialog() { dialog.value = false; }
function resetForm() { currentStep.value = 1; serverUrl.value = ''; slug.value = ''; discoveryHeaders.value = {}; serverName.value = ''; description.value = ''; websiteUrl.value = ''; email.value = ''; discoveredInfo.value = null; discoveredTools.value = []; discoveredPtrotocol.value = null; fetchError.value = ''; slugError.value = ''; isCheckingSlug.value = false; isDiscovering.value = false; isLoading.value = false; wasSlugAutoGenerated.value = false; formRef.value?.resetValidation(); }
function goBackToStep1() { currentStep.value = 1; fetchError.value = ''; }

// --- Slug Logic (remains the same) ---
function autoGenerateSlug() { slugError.value = ''; isCheckingSlug.value = false; if (!serverUrl.value || (slug.value && !wasSlugAutoGenerated.value)) { if (slug.value && !wasSlugAutoGenerated.value) { checkSlugUniquenessDebounced(); } return; } try { const url = new URL(serverUrl.value); let potentialSlug = url.hostname.toLowerCase().replace(/^www\./, '').replace(/[._]/g, '-').replace(/[^a-z0-9-]+/g, '').replace(/^-+|-+$/g, '') || 'server'; slug.value = potentialSlug; wasSlugAutoGenerated.value = true; checkSlugUniquenessDebounced(); } catch { slug.value = ''; wasSlugAutoGenerated.value = false; } }
const checkSlugUniqueness = async () => { if (!slug.value || rules.slugFormat(slug.value) !== true) { slugError.value = ''; isCheckingSlug.value = false; formRef.value?.validate(); return; } isCheckingSlug.value = true; slugError.value = ''; try { await new Promise(resolve => setTimeout(resolve, 50)); const response = await $api.getJson<{ exists: boolean }>(`/servers/check-slug/${slug.value}`); slugError.value = response.exists ? 'This slug is already taken.' : ''; } catch (error) { console.error('Error checking slug:', error); slugError.value = 'Could not verify slug.'; } finally { isCheckingSlug.value = false; formRef.value?.validate(); } };
const checkSlugUniquenessDebounced = useDebounceFn(checkSlugUniqueness, 350);
const slugUniqueRule = () => { if (isCheckingSlug.value) return 'Checking...'; return slugError.value || true; };
function handleSlugInput() { wasSlugAutoGenerated.value = false; slugError.value = ''; isCheckingSlug.value = false; checkSlugUniquenessDebounced(); }

// --- Fetch Server Info (remains the same) ---
async function fetchServerInfo() { const validationResult = await formRef.value?.validate(); if (!validationResult?.valid || isCheckingSlug.value || !!slugError.value) { showError("Please fix form errors."); return; } isDiscovering.value = true; fetchError.value = ''; discoveredInfo.value = null; discoveredTools.value = []; discoveredPtrotocol.value = null; try { const gatewayAddress = $settings.get('general_gateway_address') as string; const discoveringHandlerPath = $settings.get('path_for_discovering_handler') as string; if (!discoveringHandlerPath) throw new Error('Gateway discovery path not configured.'); const effectiveGatewayAddress = gatewayAddress || window.location.origin; const discoveryUrlPath = discoveringHandlerPath.startsWith('/') ? discoveringHandlerPath : `/${discoveringHandlerPath}`; const discoveryUrl = `${effectiveGatewayAddress}${discoveryUrlPath}`; const headersToSend = Object.entries(discoveryHeaders.value).filter(([k, v]) => k.trim() !== '' && v.trim() !== '').reduce((obj, [k, v]) => { obj[k.trim()] = v; return obj; }, {} as Record<string, string>); const requestPayload = { targetUrl: serverUrl.value, headers: headersToSend }; const data = await $api.postJsonByRawURL<DiscoveringResponse>(discoveryUrl, requestPayload); discoveredInfo.value = data; if (data.error) { discoveredPtrotocol.value = 'ERROR'; fetchError.value = `Discovery failed: ${data.error}`; } else if (data.protocol) { discoveredPtrotocol.value = data.protocol as ServerProtocol; } else { discoveredPtrotocol.value = 'UNKNOWN'; fetchError.value = 'Could not determine server type.'; } if (discoveredPtrotocol.value === 'MCP') { serverName.value = data.name || slug.value || 'MCP Server'; description.value = data.description || ''; websiteUrl.value = data.website || ''; discoveredTools.value = data.mcpTools || []; } else if (discoveredPtrotocol.value === 'A2A') { serverName.value = data.name || slug.value || 'A2A Agent'; description.value = data.description || ''; websiteUrl.value = data.website || ''; } else if (discoveredPtrotocol.value === 'REST') { serverName.value = data.name || slug.value || 'REST API'; description.value = data.description || ''; websiteUrl.value = data.website || ''; } currentStep.value = 2; } catch (error: unknown) { const message = error instanceof Error ? error.message : 'Failed to discover server info.'; fetchError.value = message; showError(message); console.error("Error discovering server:", error); discoveredPtrotocol.value = 'ERROR'; } finally { isDiscovering.value = false; } }

// --- Save Server (UPDATED types for paramSchema) ---
async function saveServer() {
    if (!['MCP', 'A2A', 'REST'].includes(discoveredPtrotocol.value || '')) { showError("Cannot add: unsupported type or discovery failed."); return; }
    const validationResult = await formRef.value?.validate();
    if (!validationResult?.valid || !isStep2Valid.value) { showError("Please fix form errors."); return; }
    if (isCheckingSlug.value || !!slugError.value) { showError('Slug invalid or checking.'); return; }
    isLoading.value = true; fetchError.value = '';
    try {
        if (!$auth.check()) throw new Error('Not logged in.');
        const user = $auth.getUser(); if (!user) throw new Error('User data missing.');

        const processedTools = discoveredPtrotocol.value === 'MCP' && discoveredInfo.value?.mcpTools ?
            discoveredInfo.value.mcpTools.map(tool => {
                let parameters: { name: string; type: string; description: string; required: boolean }[] = [];
                // Use the specific JsonSchemaProperty interface here
                if (tool.inputSchema?.properties) {
                    const requiredParams = new Set(tool.inputSchema.required || []);
                    parameters = Object.entries(tool.inputSchema.properties).map(([paramName, paramSchema]) => ({
                        name: paramName,
                        // Use type assertion or check type property
                        type: paramSchema?.type || 'string',
                        description: paramSchema?.description || '',
                        required: requiredParams.has(paramName)
                    }));
                }
                return { name: tool.name, description: tool.description || '', parameters };
            }) : [];

        const processedA2ASkills = discoveredPtrotocol.value === 'A2A' && discoveredInfo.value?.a2aSkills ? discoveredInfo.value.a2aSkills.map(skill => ({ id: skill.id || skill.name.toLowerCase().replace(/\s+/g, '-'), name: skill.name, description: skill.description || '', tags: skill.tags || [], examples: skill.examples || [], inputModes: skill.inputModes || ['text'], outputModes: skill.outputModes || ['text'] })) : [];
        const processedRESTEndpoints = discoveredPtrotocol.value === 'REST' && discoveredInfo.value?.restEndpoints ? discoveredInfo.value.restEndpoints.map((endpoint: any) => ({ path: endpoint.path, method: endpoint.method || 'GET', description: endpoint.description || '', queryParams: (endpoint.queryParams || []).map((param: any) => ({ name: param.name, type: param.type || 'string', description: param.description || '', required: param.required || false })), requestBody: endpoint.requestBody ? { description: endpoint.requestBody.description || '', example: endpoint.requestBody.example || '' } : null, responses: (endpoint.responses || []).map((response: any) => ({ statusCode: response.statusCode || 200, description: response.description || '', example: response.example || '' })) })) : [];
        const payload = { name: serverName.value, slug: slug.value, protocol: discoveredPtrotocol.value, protocolVersion: discoveredInfo.value?.protocolVersion || "", description: description.value || null, website: websiteUrl.value || null, email: email.value || user.email || null, imageUrl: null, serverUrl: serverUrl.value, tools: processedTools, a2aSkills: processedA2ASkills, restEndpoints: processedRESTEndpoints }; const createdServer = await $api.postJson<{ slug: string }>('/servers', payload); dialog.value = false; emit('server-added', createdServer); showSuccess(`${discoveredPtrotocol.value} Server added!`); if (createdServer?.slug) { router.push(`/servers/${createdServer.slug}`); } else { router.push('/servers'); } } catch (error: unknown) { const message = error instanceof Error ? error.message : 'Failed to save server.'; fetchError.value = message; showError(message); console.error("Error saving server:", error); } finally { isLoading.value = false; }
}

// --- Handle Submit (remains the same) ---
function handleSubmit() { if (currentStep.value === 1 && isStep1Valid.value && !isCheckingSlug.value) { fetchServerInfo(); } else if (currentStep.value === 2 && isStep2Valid.value) { saveServer(); } }
</script>