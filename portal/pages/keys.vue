<template>
  <div>
    <div class="d-flex justify-space-between align-center mb-4">
      <h1 class="text-h3">API Keys</h1>
      <v-btn
        color="primary"
        prepend-icon="mdi-key-plus"
        :loading="isCreating"
        @click="openCreateDialog"
      >
        Create API Key
      </v-btn>
    </div>

    <div v-if="isLoadingList" class="d-flex justify-center py-12">
      <v-progress-circular indeterminate color="primary"/>
    </div>

    <v-alert v-else-if="fetchError" type="error" variant="tonal" class="mt-4">
        {{ fetchError }}
    </v-alert>

    <v-table v-else-if="apiKeys.length > 0">
      <thead>
        <tr>
          <th>Name</th>
          <th>Key</th>
          <th>Created</th>
          <th>Last Used</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="key in apiKeys" :key="key.id">
          <td>{{ key.name }}</td>
          <td>
            <div class="d-flex align-center">
              <code v-if="key.fullKeyValue">{{ key.fullKeyValue }}</code>
              <code v-else>{{ key.maskedKey }}</code>
            </div>
          </td>
          <td>{{ formatDate(key.createdAt) }}</td>
          <td>{{ key.lastUsed ? formatDate(key.lastUsed) : 'Never' }}</td>
          <td>
            <v-tooltip location="top" text="Delete Key">
               <template #activator="{ props }">
                    <v-btn
                      v-bind="props"
                      icon
                      variant="text"
                      color="error"
                      :loading="isDeletingKey[key.id]"
                      @click="deleteApiKey(key.id)"
                    >
                      <v-icon>mdi-delete</v-icon>
                    </v-btn>
               </template>
            </v-tooltip>
          </td>
        </tr>
      </tbody>
    </v-table>
    <v-alert
      v-else
      type="info"
      variant="tonal"
      class="mt-4"
    >
      You don't have any API keys yet. Click "Create API Key" to add one.
    </v-alert>

    <v-dialog v-model="createDialog" max-width="500">
      <v-card>
        <v-card-title>Create API Key</v-card-title>
        <v-form ref="createFormRef" @submit.prevent="saveApiKey">
            <v-card-text>
              <v-text-field
                v-model="newApiKeyName"
                label="Key Name (e.g., 'My App Key')"
                required
                :rules="[rules.required]"
                variant="outlined"
                :disabled="isCreating"
              />
              
              <v-text-field
                v-model="generatedApiKey"
                label="API Key"
                readonly
                variant="outlined"
                :type="showNewKeyValue ? 'text' : 'password'"
                :append-inner-icon="showNewKeyValue ? 'mdi-eye-off' : 'mdi-eye'"
                @click:append-inner="toggleNewKeyVisibility"
              >
                <template #append>
                  <v-tooltip location="top" text="Copy API Key">
                    <template #activator="{ props }">
                      <v-btn
                        v-bind="props"
                        icon
                        size="small"
                        variant="text"
                        :loading="isCopyingNewKey"
                        @click="copyNewApiKey"
                      >
                        <v-icon>mdi-content-copy</v-icon>
                      </v-btn>
                    </template>
                  </v-tooltip>
                </template>
              </v-text-field>
            </v-card-text>
            <v-card-actions>
              <v-spacer/>
              <v-btn
                color="grey-darken-1"
                variant="text"
                :disabled="isCreating"
                @click="createDialog = false"
              >
                Cancel
              </v-btn>
              <v-btn
                color="primary"
                type="submit"
                :loading="isCreating"
                :disabled="!hasViewedOrCopiedKey"
              >
                Save
              </v-btn>
            </v-card-actions>
        </v-form>
      </v-card>
    </v-dialog>

    <v-dialog v-model="newKeyDialog" max-width="600" persistent>
      <v-card>
        <v-card-title>Your New API Key</v-card-title>
        <v-card-text>
          <v-alert type="warning" variant="tonal" density="compact" class="mb-4">
            Please copy your API key now. You won't be able to see the full key again after closing this dialog!
          </v-alert>
          <v-text-field
            :model-value="generatedApiKey"
            label="API Key"
            readonly
            variant="outlined"
            append-inner-icon="mdi-content-copy"
            @click:append-inner="copyToClipboard(generatedApiKey, 'API key copied!')"
          />
        </v-card-text>
        <v-card-actions>
          <v-spacer/>
          <v-btn
            color="primary"
            variant="tonal"
            @click="closeNewKeyDialog"
          >
            I Have Copied My Key
          </v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, reactive, watch } from 'vue';
import { useSnackbar } from '~/composables/useSnackbar';
import { rules } from '~/utils/validation'; 

definePageMeta({
  title: 'My API Keys', // Updated title
  layout: 'default',
  middleware: ['auth'],
});

// Interface for API Key data received from the LIST endpoint
interface ApiKeyListItem {
  id: string;
  name: string;
  keyPrefix: string; // e.g., "gk_abcd"
  keySuffix: string; // e.g., "wxyz"
  createdAt: string;
  lastUsed?: string | null; // Allow null
}

// Interface for API Key data received from the GET /keys/[id] or POST /keys endpoint
interface ApiKeyDetail extends Omit<ApiKeyListItem, 'keyPrefix' | 'keySuffix'>{
    key: string; // Full key value
}

// Interface for the local state which combines list info and potentially the full key
interface ApiKeyLocalState extends ApiKeyListItem {
    maskedKey: string; // Generated from prefix/suffix
    fullKeyValue?: string | null; // Store the full key once fetched
}


// --- Component State ---
const createDialog = ref(false);
const newKeyDialog = ref(false);
const newApiKeyName = ref('');
const generatedApiKey = ref(''); // Stores the newly created full key
const showNewKeyValue = ref(false); // Controls visibility of the API key
const hasViewedOrCopiedKey = ref(false); // Tracks if user has viewed or copied the key
const isCopyingNewKey = ref(false); // Loading state for copying the new key
const isLoadingList = ref(true);
const isCreating = ref(false);
const isDeletingKey = reactive<Record<string, boolean>>({}); // Loading state for deleting individual keys
const apiKeys = ref<ApiKeyLocalState[]>([]);
const fetchError = ref<string | null>(null);
const createFormRef = ref<HTMLFormElement | null>(null); // For validation

const { showSuccess, showError } = useSnackbar();
const { $api } = useNuxtApp();

// --- Lifecycle Hooks ---
onMounted(async () => {
  await fetchApiKeys();
});

// --- Utility Functions ---
function formatDate(dateString?: string | null): string {
  if (!dateString) return 'Never';
  try {
    const date = new Date(dateString);
    return date.toLocaleDateString() + ' ' + date.toLocaleTimeString();
  } catch {
    return 'Invalid Date';
  }
}

function createMaskedKey(prefix: string, suffix: string): string {
    if (!prefix && !suffix) return '********';
    return `${prefix}********${suffix}`;
}

async function copyToClipboard(text: string, successMessage: string) {
  try {
    await navigator.clipboard.writeText(text);
    showSuccess(successMessage);
  } catch (err) {
    showError('Failed to copy to clipboard.');
    console.error('Clipboard copy error:', err);
  }
}

function toggleNewKeyVisibility() {
  showNewKeyValue.value = !showNewKeyValue.value;
  if (showNewKeyValue.value === true) {
    hasViewedOrCopiedKey.value = true;
  }
}

// --- API Functions ---
async function fetchApiKeys() {
  isLoadingList.value = true;
  fetchError.value = null;
  try {
    const keysData = await $api.getJson<ApiKeyListItem[]>('/keys');
    apiKeys.value = keysData.map(key => ({
        ...key,
        maskedKey: createMaskedKey(key.keyPrefix, key.keySuffix),
        fullKeyValue: null, // Initialize full key as null
    }));
  } catch (error: unknown) {
     const message = error instanceof Error ? error.message : 'Failed to load API keys.';
     fetchError.value = message;
     showError(message);
     console.error('Error fetching API keys:', error);
     apiKeys.value = []; // Clear keys on error
  } finally {
    isLoadingList.value = false;
  }
}

function openCreateDialog() {
  // Pre-fill with current date and time
  const now = new Date();
  newApiKeyName.value = `Key ${now.toLocaleDateString()} ${now.toLocaleTimeString()}`;
  
  // Generate a new API key
  const prefix = 'g4_';
  const randomPart = Array.from({ length: 24 }, () => 
    'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'.charAt(
      Math.floor(Math.random() * 62)
    )
  ).join('');
  
  generatedApiKey.value = prefix + randomPart;
  showNewKeyValue.value = false;
  hasViewedOrCopiedKey.value = false;
  createDialog.value = true;
}

async function copyNewApiKey() {
  isCopyingNewKey.value = true;
  try {
    await navigator.clipboard.writeText(generatedApiKey.value);
    showSuccess('API key copied to clipboard!');
    hasViewedOrCopiedKey.value = true;
  } catch (error) {
    showError('Failed to copy to clipboard.');
    console.error('Error copying new API key:', error);
  } finally {
    isCopyingNewKey.value = false;
  }
}

async function getSHA256(str: string): Promise<string> {
  const encoder = new TextEncoder();
  const data = encoder.encode(str);
  const hashBuffer = await crypto.subtle.digest('SHA-256', data);
  const hashArray = Array.from(new Uint8Array(hashBuffer));
  const hashHex = hashArray.map(b => b.toString(16).padStart(2, '0')).join('');
  return hashHex;
}

async function saveApiKey() {
  // Validate form first
  if (!createFormRef.value) return;
  const { valid } = await createFormRef.value.validate();
  if (!valid) return;

  if (!newApiKeyName.value || !generatedApiKey.value || !hasViewedOrCopiedKey.value) return;

  isCreating.value = true;
  try {
    // Simple hash for the API key (this would be more secure in a real app)
    const keyHash = await getSHA256(generatedApiKey.value);

    // Create the API key
    const newKey = await $api.postJson<ApiKeyDetail>('/keys', {
      name: newApiKeyName.value,
      keyHash: keyHash
    });

    // Add the key to the list
    apiKeys.value.unshift({
      id: newKey.id,
      name: newKey.name,
      keyPrefix: generatedApiKey.value.substring(0, generatedApiKey.value.indexOf('_') + 5), // gk_XXXX
      keySuffix: generatedApiKey.value.substring(generatedApiKey.value.length - 4), // Last 4 chars
      maskedKey: createMaskedKey(
        generatedApiKey.value.substring(0, generatedApiKey.value.indexOf('_') + 5),
        generatedApiKey.value.substring(generatedApiKey.value.length - 4)
      ),
      createdAt: newKey.createdAt,
      lastUsed: newKey.lastUsed,
      fullKeyValue: null
    });

    createDialog.value = false;
    showSuccess('API key created successfully.');
  } catch (error: unknown) {
    const message = error instanceof Error ? error.message : 'Failed to create API key.';
    showError(message);
    console.error('Error creating API key:', error);
  } finally {
    isCreating.value = false;
  }
}

async function deleteApiKey(keyId: string) {
  // Use confirm for simplicity, replace with a dialog for better UX
  if (confirm('Are you sure you want to delete this API key? This action cannot be undone.')) {
    isDeletingKey[keyId] = true;
    try {
      await $api.deleteJson(`/keys/${keyId}`);

      // Remove from local state
      apiKeys.value = apiKeys.value.filter(k => k.id !== keyId);
      showSuccess('API key deleted successfully.');

    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : 'Failed to delete API key.';
      showError(message);
      console.error('Error deleting API key:', error);
    } finally {
      isDeletingKey[keyId] = false;
    }
  }
}

function closeNewKeyDialog() {
    newKeyDialog.value = false;
}

// Watch for changes in showNewKeyValue
watch(showNewKeyValue, (newValue) => {
  if (newValue === true) {
    // If key is now visible, set the flag
    hasViewedOrCopiedKey.value = true;
  }
});
</script>

<style scoped>
/* Add small spacing to icons within the key cell */
.d-flex.align-center .v-btn {
  margin-left: 4px;
}
code {
    font-family: monospace;
    background-color: #f5f5f5; /* Light grey background */
    padding: 2px 4px;
    border-radius: 4px;
    word-break: break-all; /* Prevent long keys from breaking layout */
}
</style>