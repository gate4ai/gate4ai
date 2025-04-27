<template>
  <v-dialog
    :model-value="modelValue"
    max-width="600px"
    persistent
    scrollable
    @update:model-value="closeDialog"
  >
    <v-card>
      <v-card-title
        >{{ editMode ? "Edit" : "Configure" }} Subscription
        Headers</v-card-title
      >

      <v-form ref="formRef" @submit.prevent="saveValues">
        <v-card-text>
          <p class="text-caption mb-4">
            Please provide values for the headers required by this server. These
            values will be sent with your requests.
          </p>

          <div
            v-if="isLoadingTemplate || isLoadingValues"
            class="text-center pa-4"
          >
            <v-progress-circular indeterminate color="primary" />
            <p class="mt-2">Loading header details...</p>
          </div>

          <v-alert
            v-else-if="loadError"
            type="error"
            density="compact"
            class="mb-4"
          >
            {{ loadError }}
          </v-alert>

          <div v-else-if="headerTemplate.length > 0">
            <div v-for="item in headerTemplate" :key="item.key" class="mb-3">
              <v-text-field
                v-model="editableValues[item.key]"
                :label="`${item.key}${item.required ? ' *' : ''}`"
                :hint="item.description || undefined"
                persistent-hint
                variant="outlined"
                density="compact"
                :rules="item.required ? [rules.required] : []"
                :disabled="isSaving"
              />
            </div>
          </div>

          <v-alert v-else type="info" density="compact">
            No specific headers are required for this subscription.
          </v-alert>

          <v-alert v-if="saveError" type="error" density="compact" class="mt-4">
            {{ saveError }}
          </v-alert>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn
            color="grey-darken-1"
            variant="text"
            :disabled="isSaving || isLoadingTemplate || isLoadingValues"
            @click="closeDialog"
          >
            Cancel
          </v-btn>
          <v-btn
            color="primary"
            type="submit"
            :loading="isSaving"
            :disabled="isLoadingTemplate || isLoadingValues"
          >
            {{ editMode ? "Save Changes" : "Confirm Subscription" }}
          </v-btn>
        </v-card-actions>
      </v-form>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, watch, computed, onMounted } from "vue";
import { rules } from "~/utils/validation";
import { useSnackbar } from "~/composables/useSnackbar";

interface TemplateItem {
  id: string;
  key: string;
  description?: string | null;
  required: boolean;
}

const props = defineProps<{
  modelValue: boolean; // Dialog visibility
  serverSlug: string; // Slug to fetch template
  subscriptionId?: string; // If provided, we are editing existing values
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: boolean): void;
  (e: "save", values: Record<string, string>): void; // Emit entered values on save
}>();

const { $api } = useNuxtApp();
const { showSuccess, showError } = useSnackbar();

const formRef = ref<HTMLFormElement | null>(null);
const isLoadingTemplate = ref(false);
const isLoadingValues = ref(false); // For fetching existing values in edit mode
const isSaving = ref(false);
const loadError = ref<string | null>(null);
const saveError = ref<string | null>(null);
const headerTemplate = ref<TemplateItem[]>([]);
const editableValues = ref<Record<string, string>>({});

const editMode = computed(() => !!props.subscriptionId);

async function fetchTemplate() {
  isLoadingTemplate.value = true;
  loadError.value = null;
  try {
    headerTemplate.value = await $api.getJson<TemplateItem[]>(
      `/servers/${props.serverSlug}/subscription-header-template`
    );
    // Initialize editableValues based on template keys
    const initialValues: Record<string, string> = {};
    for (const item of headerTemplate.value) {
      initialValues[item.key] = ""; // Default to empty string
    }
    editableValues.value = initialValues;
  } catch (err) {
    loadError.value =
      err instanceof Error ? err.message : "Failed to load header template.";
    showError(loadError.value);
  } finally {
    isLoadingTemplate.value = false;
  }
}

async function fetchExistingValues() {
  if (!editMode.value || !props.subscriptionId) return;
  isLoadingValues.value = true;
  loadError.value = null; // Reset error
  try {
    const existingValues = await $api.getJson<Record<string, string>>(
      `/subscriptions/${props.subscriptionId}/headers`
    );
    // Merge existing values with defaults from template
    const initialValues: Record<string, string> = {};
    for (const item of headerTemplate.value) {
      initialValues[item.key] = existingValues[item.key] ?? ""; // Use existing or default empty
    }
    editableValues.value = initialValues;
  } catch (err) {
    loadError.value =
      err instanceof Error
        ? err.message
        : "Failed to load existing header values.";
    showError(loadError.value);
  } finally {
    isLoadingValues.value = false;
  }
}

function closeDialog() {
  emit("update:modelValue", false);
  // Reset state if needed when dialog closes without saving
  headerTemplate.value = [];
  editableValues.value = {};
  loadError.value = null;
  saveError.value = null;
}

async function saveValues() {
  saveError.value = null;
  if (!formRef.value) return;

  const { valid } = await formRef.value.validate();
  if (!valid) {
    showError("Please fill in all required fields.");
    return;
  }

  isSaving.value = true;
  try {
    // Filter out empty non-required values? Optional, API should handle it.
    const valuesToSave = { ...editableValues.value };

    if (editMode.value && props.subscriptionId) {
      // --- Edit existing subscription headers ---
      await $api.putJson(
        `/subscriptions/${props.subscriptionId}/headers`,
        valuesToSave
      );
      showSuccess("Subscription headers updated successfully.");
      emit("save", valuesToSave); // Emit saved values (parent might refetch)
      closeDialog();
    } else {
      // --- Confirming new subscription ---
      // Emit the values back to the parent (SubscribeButton) to be included in the POST /subscriptions request
      emit("save", valuesToSave);
      // Dialog will be closed by the parent after successful subscription
    }
  } catch (err: unknown) {
    const message =
      err instanceof Error ? err.message : "Failed to save header values.";
    saveError.value = message;
    showError(message);
  } finally {
    isSaving.value = false;
  }
}

// Fetch data when the dialog opens or relevant props change
watch(
  () => props.modelValue,
  async (isOpen) => {
    if (isOpen) {
      loadError.value = null;
      saveError.value = null;
      await fetchTemplate();
      // If template loaded successfully and in edit mode, fetch existing values
      if (!loadError.value && editMode.value) {
        await fetchExistingValues();
      }
    }
  },
  { immediate: true }
);
</script>
