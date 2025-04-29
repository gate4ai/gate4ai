<template>
  <div class="settings-container">
    <h1 class="text-h3 mb-6">Settings</h1>

    <div class="alerts-container">
      <v-alert
        v-if="error"
        type="error"
        class="alert-overlay"
        closable
        data-testid="settings-error-alert"
        @click:close="error = ''"
      >
        {{ error }}
      </v-alert>

      <v-alert
        v-if="success"
        type="success"
        class="alert-overlay"
        closable
        data-testid="settings-success-alert"
        @click:close="success = ''"
      >
        {{ success }}
      </v-alert>

      <!-- Access Denied Alert for Env Vars (shown if non-admin tries to access) -->
      <v-alert
        v-if="envVarsError && envVarsError.includes('Forbidden')"
        type="warning"
        class="alert-overlay"
        closable
        data-testid="settings-env-forbidden-alert"
        @click:close="envVarsError = ''"
      >
        {{ envVarsError }}
      </v-alert>
      <!-- Generic Fetch Error for Env Vars -->
      <v-alert
        v-else-if="envVarsError"
        type="error"
        class="alert-overlay"
        closable
        data-testid="settings-env-error-alert"
        @click:close="envVarsError = ''"
      >
        Failed to load environment variables: {{ envVarsError }}
      </v-alert>
    </div>

    <v-card>
      <v-tabs v-model="activeTab" grow data-testid="settings-tabs">
        <v-tab
          v-for="group in settingGroups"
          :key="group"
          :value="group"
          :data-testid="`settings-tab-${group}`"
        >
          {{ formatGroupName(group) }}
        </v-tab>
        <!-- Environment Tab (conditionally shown or disabled) -->
        <v-tab
          value="environment"
          :disabled="!isAdminOrSecurity"
          data-testid="settings-tab-environment"
        >
          Environment
          <v-tooltip
            v-if="!isAdminOrSecurity"
            activator="parent"
            location="top"
          >
            Requires Admin/Security role
          </v-tooltip>
        </v-tab>
      </v-tabs>

      <v-card-text>
        <v-window v-model="activeTab" data-testid="settings-tab-content">
          <!-- Existing Settings Groups -->
          <v-window-item
            v-for="group in settingGroups"
            :key="group"
            :value="group"
            :data-testid="`settings-content-${group}`"
          >
            <v-list>
              <v-list-item
                v-for="setting in getSettingsByGroup(group)"
                :key="setting.key"
                class="setting-item"
                :data-testid="`setting-item-${setting.key}`"
              >
                <div class="setting-content">
                  <div class="setting-info">
                    <v-list-item-title class="font-weight-bold">{{
                      setting.name
                    }}</v-list-item-title>
                    <v-list-item-subtitle>{{
                      setting.description
                    }}</v-list-item-subtitle>
                  </div>

                  <div class="setting-value">
                    <!-- Boolean value -->
                    <v-switch
                      v-if="typeof setting.value === 'boolean'"
                      v-model="editedValues[setting.key]"
                      hide-details
                      :loading="isUpdating[setting.key]"
                      :disabled="isUpdating[setting.key]"
                      color="primary"
                      :data-testid="`setting-input-${setting.key}`"
                      @update:model-value="updateSetting(setting.key, $event)"
                    />

                    <!-- String value -->
                    <v-text-field
                      v-else-if="typeof setting.value === 'string'"
                      v-model="editedValues[setting.key]"
                      hide-details
                      variant="outlined"
                      density="compact"
                      :loading="isUpdating[setting.key]"
                      :disabled="isUpdating[setting.key]"
                      :data-testid="`setting-input-${setting.key}`"
                      @blur="updateSettingIfChanged(setting.key)"
                    />

                    <!-- Number value -->
                    <v-text-field
                      v-else-if="typeof setting.value === 'number'"
                      v-model.number="editedValues[setting.key]"
                      type="number"
                      hide-details
                      variant="outlined"
                      density="compact"
                      :loading="isUpdating[setting.key]"
                      :disabled="isUpdating[setting.key]"
                      :data-testid="`setting-input-${setting.key}`"
                      @blur="updateSettingIfChanged(setting.key)"
                    />

                    <!-- Object/Array/Complex value -->
                    <div v-else>
                      <v-btn
                        color="primary"
                        size="small"
                        variant="outlined"
                        :disabled="isUpdating[setting.key]"
                        :data-testid="`setting-edit-json-${setting.key}`"
                        @click="openEditDialog(setting)"
                      >
                        Edit JSON
                      </v-btn>
                    </div>
                  </div>
                </div>
              </v-list-item>
            </v-list>
          </v-window-item>

          <!-- Environment Variables Window Item -->
          <v-window-item
            key="environment"
            value="environment"
            data-testid="settings-content-environment"
          >
            <div v-if="!isAdminOrSecurity" class="text-center pa-4">
              <v-icon size="large" color="warning"
                >mdi-lock-alert-outline</v-icon
              >
              <p class="mt-2">
                Access Denied: Viewing environment variables requires Admin or
                Security role.
              </p>
            </div>
            <div
              v-else-if="isLoadingEnvVars"
              class="d-flex justify-center pa-10"
            >
              <v-progress-circular indeterminate color="primary" />
            </div>
            <div v-else-if="envVars">
              <v-alert
                type="warning"
                density="compact"
                variant="tonal"
                class="mb-4"
              >
                Displaying host environment variables. Handle with care.
              </v-alert>
              <v-table density="compact" data-testid="env-vars-table">
                <thead>
                  <tr>
                    <th class="text-left">Variable</th>
                    <th class="text-left">Value</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="(value, key) in sortedEnvVars"
                    :key="key"
                    :data-testid="`env-var-row-${key}`"
                  >
                    <td>
                      <code :data-testid="`env-var-key-${key}`">{{ key }}</code>
                    </td>
                    <td>
                      <pre
                        class="env-value"
                        :data-testid="`env-var-value-${key}`"
                        >{{ value }}</pre
                      >
                    </td>
                  </tr>
                </tbody>
              </v-table>
            </div>
            <div v-else class="text-center pa-4">
              <p>Could not load environment variables.</p>
              <v-btn
                color="primary"
                variant="text"
                data-testid="retry-fetch-env-vars"
                @click="fetchEnvVars"
                >Retry</v-btn
              >
            </div>
          </v-window-item>
        </v-window>
      </v-card-text>
    </v-card>

    <!-- JSON Dialog -->
    <v-dialog v-model="editDialog" max-width="600px">
      <v-card data-testid="edit-json-dialog">
        <v-card-title>Edit {{ currentSetting?.name }}</v-card-title>
        <v-card-text>
          <v-textarea
            v-model="complexValueText"
            rows="10"
            label="JSON Value"
            :error-messages="jsonError"
            data-testid="edit-json-textarea"
          />
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn
            color="error"
            data-testid="edit-json-cancel-button"
            @click="editDialog = false"
            >Cancel</v-btn
          >
          <v-btn
            color="primary"
            data-testid="edit-json-save-button"
            @click="saveComplexValue"
            >Save</v-btn
          >
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from "vue";
import { useRouter } from "vue-router";
import { useNuxtApp } from "#app";
import { useSnackbar } from "~/composables/useSnackbar";
// Explicitly type NuxtApp to potentially help TypeScript inference
import type { NuxtApp } from "#app";

interface Setting {
  id: string;
  key: string;
  group: string;
  name: string;
  description: string;
  value: unknown; // Keep as unknown for flexibility
  createdAt: string;
  updatedAt: string;
}

const router = useRouter();
// Explicitly type the NuxtApp instance
const { $auth, $api } = useNuxtApp() as NuxtApp;
const settings = ref<Setting[]>([]);
const editedValues = ref<Record<string, unknown>>({});
const originalValues = ref<Record<string, unknown>>({}); // Store original values for comparison
const error = ref("");
const success = ref("");
const activeTab = ref("general"); // Default tab
const editDialog = ref(false);
const currentSetting = ref<Setting | null>(null);
const complexValueText = ref("");
const jsonError = ref("");
const isUpdating = ref<Record<string, boolean>>({});
const { showSuccess, showError } = useSnackbar();

// State for Environment Variables
const envVars = ref<Record<string, string> | null>(null);
const isLoadingEnvVars = ref(false);
const envVarsError = ref<string | null>(null);

const isAdminOrSecurity = computed(() => $auth.isSecurityOrAdmin());

// Fetch core settings on mount
onMounted(async () => {
  // Redirect if not admin/security (more robust check than just isAdmin)
  if (!isAdminOrSecurity.value) {
    showError("Access Denied: Settings page requires Admin or Security role.");
    router.push("/"); // Redirect to home or another appropriate page
    return;
  }
  await fetchCoreSettings();
  // Do not fetch env vars here, wait for tab activation
});

// Get core settings from the server
async function fetchCoreSettings() {
  try {
    error.value = "";
    // Use $api which is now typed
    const data = await $api.getJson<{ settings: Setting[] }>("/settings/"); // Assuming response structure
    settings.value = data.settings || [];

    // Initialize edited and original values
    settings.value.forEach((setting) => {
      editedValues.value[setting.key] = JSON.parse(
        JSON.stringify(setting.value)
      ); // Deep copy
      originalValues.value[setting.key] = JSON.parse(
        JSON.stringify(setting.value)
      ); // Deep copy
    });
  } catch (err: unknown) {
    const errorMessage =
      err instanceof Error ? err.message : "Unknown error fetching settings";
    error.value = errorMessage;
    showError(`Failed to load core settings: ${errorMessage}`);
  }
}

// Fetch Environment Variables
async function fetchEnvVars() {
  // Only fetch if user has permission and data isn't already loaded/loading
  if (
    !isAdminOrSecurity.value ||
    envVars.value !== null ||
    isLoadingEnvVars.value
  ) {
    return;
  }

  isLoadingEnvVars.value = true;
  envVarsError.value = null;
  console.log("Fetching environment variables..."); // Debug log

  try {
    // Use $api which is now typed
    const data = await $api.getJson<Record<string, string>>("/settings/env");
    envVars.value = data;
    console.log("Environment variables loaded."); // Debug log
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : "Unknown error";
    envVarsError.value = message;
    console.error("Error fetching environment variables:", message); // Debug log
    // Don't show snackbar here, alert is shown in the template
  } finally {
    isLoadingEnvVars.value = false;
  }
}

// Watch active tab to load Env Vars
watch(activeTab, (newTab, _oldTab) => {
  if (newTab === "environment") {
    fetchEnvVars(); // Fetch when tab becomes active
  }
});

// Computed property to sort environment variables by key
const sortedEnvVars = computed(() => {
  if (!envVars.value) return {};
  return Object.entries(envVars.value)
    .sort(([keyA], [keyB]) => keyA.localeCompare(keyB))
    .reduce((obj, [key, value]) => {
      obj[key] = value;
      return obj;
    }, {} as Record<string, string>);
});

// Get groups from settings
const settingGroups = computed(() => {
  const groups = new Set(settings.value.map((s) => s.group));
  return Array.from(groups);
});

// Filter settings by group
function getSettingsByGroup(group: string) {
  return settings.value.filter((s) => s.group === group);
}

// Format group name for display
function formatGroupName(group: string) {
  if (!group) return "";
  return group.charAt(0).toUpperCase() + group.slice(1).replace(/_/g, " ");
}

// Update setting only if value has changed
async function updateSettingIfChanged(key: string) {
  const originalValue = JSON.stringify(originalValues.value[key]);
  const editedValue = JSON.stringify(editedValues.value[key]);

  if (originalValue !== editedValue) {
    await updateSetting(key, editedValues.value[key]);
  } else {
    // console.log(`Setting ${key} not changed, skipping update.`);
  }
}

// Update a setting (accepts explicit value for switch compatibility)
async function updateSetting(key: string, valueToUpdate: unknown) {
  if (isUpdating.value[key]) return; // Prevent concurrent updates

  isUpdating.value[key] = true;
  error.value = "";
  success.value = "";

  try {
    // Perform the API call using typed $api
    const updatedData = await $api.putJson<{ setting: Setting }>(
      `/settings/${key}`,
      {
        value: valueToUpdate,
      }
    );

    // Update local state on success
    const index = settings.value.findIndex((s) => s.key === key);
    if (index !== -1) {
      settings.value[index].value = updatedData.setting.value; // Update with value from response
      // Update both original and edited values to reflect the saved state
      originalValues.value[key] = JSON.parse(
        JSON.stringify(updatedData.setting.value)
      );
      editedValues.value[key] = JSON.parse(
        JSON.stringify(updatedData.setting.value)
      );
    }

    showSuccess(`Setting '${key}' updated successfully`);
  } catch (err: unknown) {
    const errorMessage =
      err instanceof Error
        ? err.message
        : `Unknown error updating setting ${key}`;
    error.value = errorMessage; // Display error in the main alert
    showError(errorMessage); // Also show snackbar
    // Revert edited value back to original on error
    editedValues.value[key] = JSON.parse(
      JSON.stringify(originalValues.value[key])
    );
  } finally {
    isUpdating.value[key] = false;
  }
}

// Open edit dialog for complex values
function openEditDialog(setting: Setting) {
  currentSetting.value = setting;
  try {
    // Pretty print JSON for editing
    complexValueText.value = JSON.stringify(
      editedValues.value[setting.key],
      null,
      2
    );
  } catch {
    complexValueText.value = String(editedValues.value[setting.key]); // Fallback to string if stringify fails
  }
  jsonError.value = "";
  editDialog.value = true;
}

// Save complex value from dialog
async function saveComplexValue() {
  if (!currentSetting.value) return;
  jsonError.value = "";

  try {
    // Validate JSON format before attempting to parse/save
    const parsedValue = JSON.parse(complexValueText.value);

    // Update edited value immediately for UI feedback
    editedValues.value[currentSetting.value.key] = parsedValue;

    // Trigger the update process (which includes API call and state update)
    await updateSetting(currentSetting.value.key, parsedValue);

    // Close dialog only if update was successful (no error thrown)
    editDialog.value = false;
  } catch (err: unknown) {
    if (err instanceof SyntaxError) {
      jsonError.value = "Invalid JSON format. Please check your syntax.";
    } else {
      const errorMessage =
        err instanceof Error ? err.message : "Error saving setting";
      jsonError.value = errorMessage; // Show error within the dialog
      showError(errorMessage); // Show snackbar as well
    }
  }
  // Note: updateSetting handles the final state updates and potential reverts
}
</script>

<style scoped>
.settings-container {
  position: relative; /* Needed for absolute positioning of alerts */
}

.alerts-container {
  /* Removed fixed positioning to keep alerts within the normal flow */
  width: 100%;
  max-width: 800px; /* Optional: Limit width */
  margin: 0 auto 16px auto; /* Center and add bottom margin */
  z-index: 10; /* Keep above content slightly */
  display: flex;
  flex-direction: column;
  gap: 8px; /* Space between multiple alerts */
}

.alert-overlay {
  width: 100%; /* Take full width of the container */
}

.setting-item {
  padding: 12px 16px !important; /* Adjust padding */
  min-height: 68px; /* Ensure consistent height */
  border-bottom: 1px solid rgba(0, 0, 0, 0.06); /* Subtle separator */
}
.setting-item:last-child {
  border-bottom: none;
}

.setting-content {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 16px; /* Add gap between info and value */
}

.setting-info {
  flex: 1; /* Allow info to take available space */
  /* Removed fixed flex basis */
}

.setting-value {
  flex-shrink: 0; /* Prevent value side from shrinking */
  /* Set a max-width or width if needed, e.g. */
  max-width: 300px;
  display: flex;
  justify-content: flex-end;
}
.setting-value .v-switch {
  flex: none; /* Prevent switch from stretching */
}
.setting-value .v-input {
  min-width: 150px; /* Ensure text fields have minimum width */
}

/* Styling for environment variables table */
code {
  background-color: #f5f5f5;
  padding: 2px 4px;
  border-radius: 4px;
  font-family: monospace;
}
pre.env-value {
  white-space: pre-wrap; /* Allow wrapping */
  word-break: break-all; /* Break long values */
  margin: 0; /* Remove default margins */
  padding: 0; /* Remove default padding */
  font-family: monospace;
  font-size: 0.875rem;
  background-color: transparent; /* Inherit background */
}
</style>
