<template>
  <v-dialog :model-value="modelValue" max-width="800px" persistent scrollable @update:model-value="closeDialog">
    <v-card>
      <v-card-title>Edit Subscription Header Template</v-card-title>
      <v-card-text>
        <!-- ... (template content as before) ... -->
         <p class="text-caption mb-4"> Define headers that subscribers need to provide... </p>
        <v-divider class="mb-4" />
        <div v-for="(item, index) in editableTemplate" :key="item.id || `new-${index}`" class="template-item mb-4 pa-3 border rounded">
           <v-row dense data-testid="template-item">
             <v-col cols="12" md="4"> 
              <v-text-field data-testid="key" v-model="item.key" label="Header Key *" variant="outlined" density="compact" :rules="[rules.required, rules.headerKeyFormat]" :disabled="isLoading" />
            </v-col>
             <v-col cols="12" md="6">
              <v-text-field data-testid="description" v-model="item.description" label="Description (for subscriber)" variant="outlined" density="compact" hide-details :disabled="isLoading" />
            </v-col>
             <v-col cols="12" md="2" class="d-flex align-center justify-space-between">
              <v-checkbox data-testid="required" v-model="item.required" label="Required" density="compact" hide-details :disabled="isLoading" class="mt-n4" />
              <v-btn icon size="small" variant="text" color="error" :disabled="isLoading" @click="removeItem(index)"> 
                <v-icon>mdi-delete</v-icon> 
              </v-btn>
            </v-col>
           </v-row>
        </div>
        <v-btn variant="text" color="primary" prepend-icon="mdi-plus" :disabled="isLoading" @click="addItem"> Add Template Header </v-btn>
        <v-alert v-if="error" type="error" density="compact" class="mt-4"> {{ error }} </v-alert>
      </v-card-text>
      <v-card-actions>
        <v-spacer />
        <v-btn color="grey-darken-1" variant="text" :disabled="isLoading" @click="closeDialog"> Cancel </v-btn>
        <v-btn color="primary" :loading="isLoading" @click="saveTemplate"> Save Template </v-btn>
      </v-card-actions>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue';
import { useSnackbar } from '~/composables/useSnackbar';

const rules = { required: (v: unknown): boolean | string => !!v || 'Field is required', headerKeyFormat: (v: string): boolean | string => /^[A-Za-z0-9-]+$/.test(v) || 'Invalid format (letters, numbers, hyphens)', };
interface TemplateItem { id?: string; key: string; description?: string | null; required: boolean; }
const props = defineProps<{ modelValue: boolean; serverSlug: string; serverUrl: string; initialTemplate: TemplateItem[]; }>();
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void; (e: 'template-updated', template: TemplateItem[]): void; }>();
const { $api } = useNuxtApp();
const { showSuccess, showError } = useSnackbar();
const isLoading = ref(false);
const error = ref<string | null>(null);
const editableTemplate = ref<TemplateItem[]>([]);

watch(() => props.initialTemplate, (newTemplate) => { editableTemplate.value = JSON.parse(JSON.stringify(newTemplate || [])); }, { immediate: true, deep: true });

function closeDialog() { emit('update:modelValue', false); }
function addItem() { editableTemplate.value.push({ key: '', description: '', required: false }); }
function removeItem(index: number) { editableTemplate.value.splice(index, 1); }
function validateTemplate(): boolean { /* ... */ error.value = null; const keys = new Set<string>(); for (let i = 0; i < editableTemplate.value.length; i++) { const item = editableTemplate.value[i]; if (!item.key || item.key.trim() === '') { error.value = `Error in item ${i + 1}: Header Key is required.`; return false; } const trimmedKey = item.key.trim(); if (!/^[A-Za-z0-9-]+$/.test(trimmedKey)) { error.value = `Error in item ${i + 1}: Invalid key format for '${trimmedKey}'. Use letters, numbers, hyphens.`; return false; } if (keys.has(trimmedKey)) { error.value = `Error: Duplicate header key found: '${trimmedKey}'. Keys must be unique.`; return false; } keys.add(trimmedKey); item.key = trimmedKey; } return true; }

async function saveTemplate() {
  if (!validateTemplate()) { if (error.value) showError(error.value); return; }
  isLoading.value = true; error.value = null;
  try {
    const templateToSend = editableTemplate.value
       .filter(item => item.key)
       .map(({ key, description, required }) => ({ key, description: description || null, required }));

    // Explicitly stringify the array for the API call
    const updated = await $api.putJson<TemplateItem[]>(
        `/servers/${props.serverSlug}/subscription-header-template`,
        JSON.stringify(templateToSend) // Stringify the array
    );

    showSuccess('Template updated.'); emit('template-updated', updated); closeDialog();
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : 'Failed to update template.';
    error.value = message; showError(message);
  } finally {
    isLoading.value = false;
  }
}
</script>

<style scoped> /* ... */ .template-item { border: 1px solid #e0e0e0; } .mt-n4 { margin-top: -16px; } </style>