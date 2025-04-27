<template>
  <v-dialog
    :model-value="modelValue"
    max-width="500px"
    persistent
    @update:model-value="closeDialog"
  >
    <v-card>
      <v-card-title class="text-h5">Upload Server Logo</v-card-title>

      <v-card-text>
        <v-file-input
          v-model="selectedFile"
          label="Select file (PNG, JPG, SVG, GIF, max 2MB)"
          accept="image/png,image/jpeg,image/svg+xml,image/gif"
          prepend-icon="mdi-camera"
          show-size
          :rules="fileRules"
          clearable
          class="mb-4"
          @click:clear="clearSelection"
        />

        <div v-if="previewUrl" class="d-flex justify-center mb-4">
          <v-img
            :src="previewUrl"
            max-height="150"
            max-width="250"
            contain
            alt="Logo preview"
          />
        </div>

        <!-- Display error message derived from validation -->
        <v-alert
          v-if="errorMessage"
          type="error"
          density="compact"
          class="mt-2"
        >
          {{ errorMessage }}
        </v-alert>
      </v-card-text>

      <v-card-actions>
        <v-spacer />
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
          variant="flat"
          :loading="isLoading"
          :disabled="!selectedFile || !!errorMessage"
          @click="uploadFile"
        >
          Upload
        </v-btn>
      </v-card-actions>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from "vue";
import { useSnackbar } from "~/composables/useSnackbar";

const props = defineProps<{
  modelValue: boolean; // Dialog visibility
  serverSlug: string;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: boolean): void;
  (e: "logo-updated", newImageUrl: string): void; // Emit the new URL on success
}>();

const { $api } = useNuxtApp();
const { showSuccess, showError } = useSnackbar();

const isLoading = ref(false);
// v-model now binds directly to this ref (File | null)
const selectedFile = ref<File | null>(null);
const previewUrl = ref<string | null>(null);
const errorMessage = ref<string | null>(null); // Error message state

const MAX_FILE_SIZE_MB = 2;
const MAX_FILE_SIZE_BYTES = MAX_FILE_SIZE_MB * 1024 * 1024;

// Client-side validation rules for v-file-input display
const fileRules = computed(() => [
  (file: File | null): boolean | string => {
    // Rule now accepts File | null
    if (!file) return true; // No file selected is valid in terms of rules
    return (
      file.size <= MAX_FILE_SIZE_BYTES ||
      `File size should be less than ${MAX_FILE_SIZE_MB} MB`
    );
  },
]);

// Watch for changes in the selected file
watch(selectedFile, (newFile, oldFile) => {
  errorMessage.value = null; // Reset error message on any change
  cleanupPreviewUrl(); // Revoke old URL if it exists

  if (newFile) {
    // Validate the newly selected file
    if (newFile.size > MAX_FILE_SIZE_BYTES) {
      errorMessage.value = `File size should be less than ${MAX_FILE_SIZE_MB} MB.`;
      // Important: Reset selectedFile back to null if invalid, so button disables
      selectedFile.value = null;
    } else {
      // File is valid, create preview
      previewUrl.value = URL.createObjectURL(newFile);
    }
  } else {
    // File was cleared or reset due to validation failure
    previewUrl.value = null;
  }
});

// Method to clear the selection explicitly (e.g., from @click:clear)
function clearSelection() {
  selectedFile.value = null; // This will trigger the watcher to clear preview and error
}

// Clean up the blob URL
function cleanupPreviewUrl() {
  if (previewUrl.value) {
    URL.revokeObjectURL(previewUrl.value);
    previewUrl.value = null;
  }
}

function closeDialog() {
  cleanupPreviewUrl();
  selectedFile.value = null; // Ensure file state is reset
  errorMessage.value = null;
  emit("update:modelValue", false);
}

async function uploadFile() {
  // This check is technically redundant because the button is disabled, but good practice
  if (!selectedFile.value || errorMessage.value) {
    showError(errorMessage.value ?? "Please select a valid file.");
    return;
  }

  isLoading.value = true;
  errorMessage.value = null; // Clear any previous submission errors

  const formData = new FormData();
  formData.append("logoFile", selectedFile.value); // Key must match backend expectation

  try {
    const response = await $api.postFormData<{ imageUrl: string }>(
      `/servers/${props.serverSlug}/logo`,
      formData
    );

    if (response && response.imageUrl) {
      showSuccess("Logo uploaded successfully!");
      emit("logo-updated", response.imageUrl); // Pass the new URL back
      closeDialog();
    } else {
      throw new Error("Invalid response received from server after upload.");
    }
  } catch (err: unknown) {
    const message =
      err instanceof Error ? err.message : "Failed to upload logo.";
    // Show the error message directly in the dialog's alert
    errorMessage.value = message;
    showError(message); // Also show in snackbar
    console.error("Logo upload error:", err);
  } finally {
    isLoading.value = false;
  }
}

// Clean up preview URL when component is unmounted
onUnmounted(() => {
  cleanupPreviewUrl();
});
</script>
