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
          <v-text-field
            v-model="serverUrl"
            label="Server URL"
            placeholder="https://example.com/mcp"
            hint="URL of the server you want to add"
            required
            :disabled="isLoading"
          />

        
        <!-- Error Message -->
        <v-alert
          v-if="error"
          type="error"
          class="mt-4"
          closable
          @click:close="error = ''"
        >
          {{ error }}
        </v-alert>
      </v-card-text>
      
      <v-card-actions>
        <v-spacer />
        <div class="d-flex justify-space-between mt-4">
            <v-btn
              color="grey-darken-1"
              variant="text"
              :disabled="isLoading"
              @click="closeDialog"
            >
              Cancel
            </v-btn>
            <v-btn
              color="primary"
              type="submit"
              :loading="isLoading"
              :disabled="!serverUrl"
            >
              Fetch Server Info
            </v-btn>
          </div>
      </v-card-actions>
      </v-form> 
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue';
import type { Tool } from '~/utils/server';
import { useSnackbar } from '~/composables/useSnackbar';

// Interface definitions
interface ServerInfo {
  serverInfo: {
    name: string;
    version?: string;
  };
  tools: Tool[];
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
const form = ref(null);

// Form fields
const serverUrl = ref('');
const serverName = ref('');
const description = ref('');
const websiteUrl = ref('');
const email = ref('');

// Server info
const serverInfo = ref<ServerInfo | null>(null);
const isLoading = ref(false);
const isSaving = ref(false);
const error = ref('');

const { showError } = useSnackbar();

// Watch for dialog changes from parent
watch(() => props.modelValue, (val) => {
  dialog.value = val;
});

// Watch for local dialog changes
watch(dialog, (val) => {
  emit('update:modelValue', val);
  if (!val) {
    resetForm();
  }
});

// Reset form to initial state
function resetForm() {
  serverUrl.value = '';
  serverName.value = '';
  description.value = '';
  websiteUrl.value = '';
  email.value = '';
  serverInfo.value = null;
  error.value = '';
}

// Close dialog
function closeDialog() {
  dialog.value = false;
}

// Fetch server info from the server
async function fetchServerInfo() {
  if (!serverUrl.value) return;
  
  isLoading.value = true;
  error.value = '';
  
  try {
    const { $settings } = useNuxtApp();
    const gatewayAddress = $settings.get('general_gateway_address') as string;
    const infoHandler = $settings.get('general_gateway_info_handler') as string;
    
    if (!infoHandler) {
      throw new Error('Gateway info handler is not configured properly');
    }
    
    // Use the frontend's address if gateway address is not set
    const effectiveGatewayAddress = gatewayAddress || window.location.origin;
    
    const { $api } = useNuxtApp();
    const data = await $api.getJsonByRawURL(effectiveGatewayAddress+infoHandler, {
            params: {
                url: serverUrl.value
            }
        });    
    if (data.error) {
      throw new Error(data.error);
    }
    
    serverInfo.value = data;
    
    // Set initial server name from server info
    if (data.serverInfo && data.serverInfo.name) {
      serverName.value = data.serverInfo.name;
      saveServer();
    }
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('Failed to add server');
    }
    console.error("Error adding server:", err);
  } finally {
    isLoading.value = false;
  }
}

// Save the server to the database
async function saveServer() {
  if (!serverInfo.value || !serverName.value) return;
  
  isSaving.value = true;
  error.value = '';
  
  try {
    const { $auth, $api } = useNuxtApp();
    
    if (!$auth.check()) {
      throw new Error('You must be logged in to add a server');
    }
    
    // Get current user
    const user = $auth.getUser();
    if (!user) {
      throw new Error('Failed to fetch user data');
    }
    
    // Use user's email if email field is empty
    if (!email.value && user.email) {
      email.value = user.email;
    }

    // Process tools to match the database schema
    const processedTools = serverInfo.value.tools.map(tool => {
      // Extract parameters from inputSchema
      let parameters: Array<{name: string; type: string; description: string; required: boolean}> = [];
      
      if (tool.inputSchema && tool.inputSchema.properties) {
        parameters = Object.entries(tool.inputSchema.properties).map(([name, schema]) => {
          const paramSchema = schema as unknown as Record<string, unknown>;
          return {
            name,
            type: paramSchema.type?.toString() || 'string',
            description: paramSchema.description?.toString() || '',
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

    const data = await $api.postJson('/servers', {
      name: serverName.value,
      description: description.value,
      website: websiteUrl.value || null,
      email: email.value || null,
      serverUrl: serverUrl.value,
      tools: processedTools,
    });
    
    // Close the dialog first
    dialog.value = false;
    
    // Emit the server-added event
    emit('server-added', data);
    
    // Then navigate to the server details page
    // Make sure 'data' returned from the POST request contains the ID
    if (data && data.id) {
      navigateTo(`/servers/${data.id}`);
    } else {
      // Handle cases where ID might not be returned or data is unexpected
      console.warn('Server created, but ID not found in response. Navigating to servers list.');
      navigateTo('/servers');
    }
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('Failed to add server');
    }
    console.error("Error adding server:", err);
  } finally {
    isSaving.value = false;
  }
}

</script>