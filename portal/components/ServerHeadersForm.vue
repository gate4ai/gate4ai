<template>
    <v-dialog :model-value="modelValue" max-width="700px" persistent scrollable @update:model-value="closeDialog">
      <v-card>
        <v-card-title>Edit Server Headers</v-card-title>
        <v-card-text>
          <p class="text-caption mb-4">
            These headers will be automatically added by the gateway to requests sent to this server's URL (<code>{{ serverUrl }}</code>).
            Subscription headers will override these if keys conflict. System headers (e.g., `Gate4ai-User-Id`) have the highest priority.
          </p>
          <KeyValueInput v-model="editableHeaders" :disabled="isLoading" />
          <v-alert v-if="error" type="error" density="compact" class="mt-4">
            {{ error }}
          </v-alert>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn color="grey-darken-1" variant="text" :disabled="isLoading" @click="closeDialog">
            Cancel
          </v-btn>
          <v-btn color="primary" :loading="isLoading" @click="saveHeaders">
            Save Headers
          </v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </template>
  
  <script setup lang="ts">
  import { ref, watch } from 'vue';
  import KeyValueInput from './KeyValueInput.vue';
  import { useSnackbar } from '~/composables/useSnackbar';
  
  const props = defineProps<{
    modelValue: boolean; // Dialog visibility
    serverSlug: string;
    serverUrl: string; // For display
    initialHeaders: Record<string, string>;
  }>();
  
  const emit = defineEmits<{
    (e: 'update:modelValue', value: boolean): void;
    (e: 'headers-updated', headers: Record<string, string>): void; // Emit updated headers
  }>();
  
  const { $api } = useNuxtApp();
  const { showSuccess, showError } = useSnackbar();
  
  const isLoading = ref(false);
  const error = ref<string | null>(null);
  const editableHeaders = ref<Record<string, string>>({});
  
  // Sync editableHeaders when initialHeaders prop changes (e.g., dialog reopens)
  watch(() => props.initialHeaders, (newHeaders) => {
    editableHeaders.value = { ...(newHeaders || {}) };
  }, { immediate: true });
  
  function closeDialog() {
    emit('update:modelValue', false);
  }
  
  async function saveHeaders() {
    isLoading.value = true;
    error.value = null;
    try {
      // Filter out empty keys before sending
      const headersToSend = Object.entries(editableHeaders.value)
        .filter(([key]) => key.trim() !== '')
        .reduce((obj, [key, value]) => {
          obj[key.trim()] = value; // Trim keys as well
          return obj;
        }, {} as Record<string, string>);
  
      const updated = await $api.putJson<Record<string, string>>(`/servers/${props.serverSlug}/headers`, headersToSend);
      showSuccess('Server headers updated successfully.');
      emit('headers-updated', updated); // Notify parent
      closeDialog();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Failed to update server headers.';
      error.value = message;
      showError(message);
    } finally {
      isLoading.value = false;
    }
  }
  </script>