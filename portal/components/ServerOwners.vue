<template>
  <div>
    <h3 class="text-h6 mb-2">Server Owners</h3>

    <!-- Loading indicator for owners list -->
    <v-progress-linear
      v-if="isListLoading"
      indeterminate
      color="primary"
      class="mb-2"
    />

    <v-alert v-if="listError" type="error" density="compact" class="mb-2">
      {{ listError }}
    </v-alert>

    <v-list v-if="!isListLoading && localOwners.length > 0">
      <v-list-item v-for="owner in localOwners" :key="owner.user.id">
        <v-list-item-title>{{ owner.user.name || "User" }}</v-list-item-title>
        <v-list-item-subtitle>{{ owner.user.email }}</v-list-item-subtitle>
        <template #append>
          <!-- Show delete button only if allowed and not the last owner -->
          <v-btn
            v-if="canManage && localOwners.length > 1"
            icon
            variant="text"
            color="error"
            size="small"
            :loading="isDeleting[owner.user.id]"
            @click="confirmRemoveOwner(owner)"
          >
            <v-icon>mdi-delete</v-icon>
          </v-btn>
        </template>
      </v-list-item>
    </v-list>
    <v-list-item v-else-if="!isListLoading">
      <v-list-item-title class="text-grey"
        >No owners assigned.</v-list-item-title
      >
    </v-list-item>

    <!-- Show Add Owner button only if allowed -->
    <v-btn
      v-if="canManage"
      color="primary"
      prepend-icon="mdi-plus"
      class="mt-2"
      @click="openAddOwnerDialog"
    >
      Add Owner
    </v-btn>

    <!-- Add Owner Dialog -->
    <v-dialog v-model="addDialog" max-width="500px">
      <v-card>
        <v-card-title>Add Server Owner</v-card-title>
        <v-card-text>
          <v-alert
            v-if="dialogErrorMessage"
            type="error"
            class="mb-4"
            density="compact"
            closable
            @click:close="dialogErrorMessage = ''"
          >
            {{ dialogErrorMessage }}
          </v-alert>

          <v-form ref="addFormRef" @submit.prevent="addOwner">
            <v-text-field
              v-model="emailToAdd"
              label="User Email"
              type="email"
              required
              :rules="[rules.required, rules.email]"
              :disabled="isActionLoading"
            />
          </v-form>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn
            color="grey-darken-1"
            variant="text"
            :disabled="isActionLoading"
            @click="addDialog = false"
          >
            Cancel
          </v-btn>
          <v-btn
            color="primary"
            variant="text"
            :loading="isActionLoading"
            type="submit"
            @click="addOwner"
          >
            Add
          </v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <!-- Delete Confirmation Dialog -->
    <v-dialog v-model="removeDialog" max-width="500px">
      <v-card>
        <v-card-title class="text-h5">Remove Owner</v-card-title>
        <v-card-text>
          Are you sure you want to remove
          <strong>{{
            ownerToRemove?.user.name || ownerToRemove?.user.email
          }}</strong>
          as an owner?
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn
            color="grey-darken-1"
            variant="text"
            @click="removeDialog = false"
            >Cancel</v-btn
          >
          <v-btn
            color="error"
            variant="text"
            :loading="isActionLoading"
            @click="removeOwner"
            >Yes, Remove</v-btn
          >
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from "vue";
import type { ServerOwner } from "~/utils/server";
import { rules } from "~/utils/validation"; // Import validation rules
import { useSnackbar } from "~/composables/useSnackbar";

const props = defineProps<{
  serverId: string;
  owners: ServerOwner[];
}>();

// Use NuxtApp for plugins
const { $auth, $api } = useNuxtApp();

// Local reactive copy of owners for updates
const localOwners = ref<ServerOwner[]>([...props.owners]);
watch(
  () => props.owners,
  (newOwners) => {
    localOwners.value = [...newOwners];
  }
);

console.log(localOwners.value);
// State for Add Dialog
const addDialog = ref(false);
const emailToAdd = ref("");
const addFormRef = ref<HTMLFormElement | null>(null); // For form validation

// State for Remove Dialog
const removeDialog = ref(false);
const ownerToRemove = ref<ServerOwner | null>(null);

// Loading and Error States
const isActionLoading = ref(false); // For add/remove actions
const isListLoading = ref(false); // Potentially needed if list is refreshed separately
const dialogErrorMessage = ref(""); // Error inside the add dialog
const listError = ref(""); // Error related to fetching/displaying the list
const isDeleting = ref<Record<string, boolean>>({}); // Track loading state per owner for delete

// --- Permissions ---
const canManage = computed(() => {
  const user = $auth.getUser();
  if (!user) return false;
  // Admin or Security can manage
  if ($auth.isSecurityOrAdmin()) return true;
  // Check if the current user is in the owner list
  return localOwners.value.some((owner) => owner.user.id === user.id);
});

// --- Add Owner Logic ---
function openAddOwnerDialog() {
  emailToAdd.value = "";
  dialogErrorMessage.value = "";
  addDialog.value = true;
}

const { showError } = useSnackbar();

async function addOwner() {
  // Validate form first
  const form = addFormRef.value;
  if (form) {
    const { valid } = await form.validate();
    if (!valid) return;
  }

  isActionLoading.value = true;
  dialogErrorMessage.value = "";
  listError.value = ""; // Clear list error too

  try {
    // Use the new POST endpoint which returns the updated list
    const updatedOwners = (await $api.postJson(
      `/servers/${props.serverId}/owners`,
      {
        email: emailToAdd.value,
      }
    )) as ServerOwner[];

    // Update local state reactively
    localOwners.value = updatedOwners;

    addDialog.value = false; // Close dialog on success
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError("Failed to update owners");
    }
    console.error("Error updating owners:", err);
  } finally {
    isActionLoading.value = false;
  }
}

// --- Remove Owner Logic ---
function confirmRemoveOwner(owner: ServerOwner) {
  ownerToRemove.value = owner;
  dialogErrorMessage.value = ""; // Clear any previous errors
  listError.value = "";
  removeDialog.value = true;
}

async function removeOwner() {
  if (!ownerToRemove.value) return;

  const ownerId = ownerToRemove.value.user.id;
  isActionLoading.value = true;
  isDeleting.value[ownerId] = true; // Show loading on the specific button
  dialogErrorMessage.value = ""; // Clear errors
  listError.value = "";

  try {
    // Use the new DELETE endpoint
    const updatedOwners = (await $api.deleteJson(
      `/servers/${props.serverId}/owners/${ownerId}`
    )) as ServerOwner[];

    // Update local state reactively
    localOwners.value = updatedOwners;

    removeDialog.value = false; // Close dialog
    ownerToRemove.value = null; // Reset owner to remove
  } catch (error: unknown) {
    if (error instanceof Error) {
      listError.value = error.message;
    } else {
      listError.value = "Failed to remove owner";
    }
    console.error("Remove owner error:", error);
    removeDialog.value = false; // Close dialog even on error
  } finally {
    isActionLoading.value = false;
    if (ownerId) {
      isDeleting.value[ownerId] = false; // Stop loading on the button
    }
  }
}
</script>
