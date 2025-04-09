<template>
  <div class="settings-container">
    <h1 class="text-h3 mb-6">Settings</h1>
    
    <div class="alerts-container">
      <v-alert v-if="error" type="error" class="alert-overlay">
        {{ error }}
      </v-alert>
      
      <v-alert v-if="success" type="success" class="alert-overlay">
        {{ success }}
      </v-alert>
    </div>

    <v-card>
      <v-tabs v-model="activeTab">
        <v-tab v-for="group in settingGroups" :key="group" :value="group">
          {{ formatGroupName(group) }}
        </v-tab>
      </v-tabs>

      <v-card-text>
        <v-window v-model="activeTab">
          <v-window-item v-for="group in settingGroups" :key="group" :value="group">
            <v-list>
              <v-list-item v-for="setting in getSettingsByGroup(group)" :key="setting.key" class="setting-item">
                <div class="setting-content">
                  <div class="setting-info">
                    <v-list-item-title class="font-weight-bold">{{ setting.name }}</v-list-item-title>
                    <v-list-item-subtitle>{{ setting.description }}</v-list-item-subtitle>
                  </div>
                  
                  <div class="setting-value">
                    <!-- Boolean value -->
                    <v-switch 
                      v-if="typeof setting.value === 'boolean'" 
                      v-model="editedValues[setting.key]"
                      hide-details
                      @update:model-value="updateSetting(setting.key)"
                    />
                    
                    <!-- String value -->
                    <v-text-field 
                      v-else-if="typeof setting.value === 'string'" 
                      v-model="editedValues[setting.key]"
                      hide-details
                      @blur="updateSetting(setting.key)"
                    />
                    
                    <!-- Number value -->
                    <v-text-field 
                      v-else-if="typeof setting.value === 'number'" 
                      v-model.number="editedValues[setting.key]"
                      type="number"
                      hide-details
                      @blur="updateSetting(setting.key)"
                    />
                    
                    <!-- Object/Array/Complex value -->
                    <div v-else>
                      <v-btn 
                        color="primary" 
                        size="small" 
                        @click="openEditDialog(setting)"
                      >
                        Edit
                      </v-btn>
                    </div>
                  </div>
                </div>
              </v-list-item>
            </v-list>
          </v-window-item>
        </v-window>
      </v-card-text>
    </v-card>

    <!-- Dialog for complex values -->
    <v-dialog v-model="editDialog" max-width="600px">
      <v-card>
        <v-card-title>Edit {{ currentSetting?.name }}</v-card-title>
        <v-card-text>
          <v-textarea
            v-model="complexValueText"
            rows="10"
            label="JSON Value"
            :error-messages="jsonError"
          />
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn color="error" @click="editDialog = false">Cancel</v-btn>
          <v-btn color="primary" @click="saveComplexValue">Save</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { useRouter } from 'vue-router';
import { useNuxtApp } from '#app';
import { useSnackbar } from '~/composables/useSnackbar';

interface Setting {
  id: string;
  key: string;
  group: string;
  name: string;
  description: string;
  value: unknown;
  createdAt: string;
  updatedAt: string;
}

const router = useRouter();
const { $auth, $api } = useNuxtApp();
const settings = ref<Setting[]>([]);
const editedValues = ref<Record<string, unknown>>({});
const error = ref('');
const success = ref('');
const activeTab = ref('general');
const editDialog = ref(false);
const currentSetting = ref<Setting | null>(null);
const complexValueText = ref('');
const jsonError = ref('');
const isUpdating = ref<Record<string, boolean>>({});
const { showSuccess, showError } = useSnackbar();

// Use auth plugin to check if user is admin
const isAdmin = computed(() => {
  return $auth.isAdmin();
});

// Redirect if not admin
onMounted(async () => {
  if (!isAdmin.value) {
    router.push('/');
    return;
  }
  
  await fetchSettings();
});

// Get settings from the server
async function fetchSettings() {
  try {
    error.value = '';

    const data = await $api.getJson('/settings/');
    
    settings.value = data.settings || [];
    
    // Initialize edited values
    settings.value.forEach(setting => {
      editedValues.value[setting.key] = setting.value;
    });
  } catch (err: unknown) {
    const errorMessage = err instanceof Error ? err.message : 'Unknown error fetching settings';
    error.value = errorMessage;
  }
}

// Get groups from settings
const settingGroups = computed(() => {
  const groups = new Set(settings.value.map(s => s.group));
  return Array.from(groups);
});

// Filter settings by group
function getSettingsByGroup(group: string) {
  return settings.value.filter(s => s.group === group);
}

// Format group name for display
function formatGroupName(group: string) {
  return group.charAt(0).toUpperCase() + group.slice(1);
}

// Update a setting
async function updateSetting(key: string) {
  try {
    isUpdating.value[key] = true;
    error.value = '';
    const editedValue = editedValues.value[key];

    await $api.putJson(`/settings/${key}`, {
      value: editedValue
    });
    
    // Update the setting in the local state
    const updatedSetting = settings.value.find(s => s.key === key);
    if (updatedSetting) {
      updatedSetting.value = editedValue;
    }
    
    showSuccess('Setting updated successfully');
  } catch (err: unknown) {
    const errorMessage = err instanceof Error ? err.message : 'Unknown error updating setting';
    error.value = errorMessage;
    showError(errorMessage);
  } finally {
    isUpdating.value[key] = false;
  }
}

// Open edit dialog for complex values
function openEditDialog(setting: Setting) {
  currentSetting.value = setting;
  complexValueText.value = JSON.stringify(setting.value, null, 2);
  jsonError.value = '';
  editDialog.value = true;
}

// Save complex value
async function saveComplexValue() {
  try {
    jsonError.value = '';
    
    // Validate JSON
    let parsedValue;
    try {
      parsedValue = JSON.parse(complexValueText.value);
    } catch {
      jsonError.value = 'Invalid JSON format';
      return;
    }
    
    if (!currentSetting.value) return;
    
    // Update in UI
    editedValues.value[currentSetting.value.key] = parsedValue;
    
    // Save to server
    await updateSetting(currentSetting.value.key);
    
    // Close dialog
    editDialog.value = false;
  } catch (err: unknown) {
    const errorMessage = err instanceof Error ? err.message : 'Unknown error saving value';
    jsonError.value = errorMessage;
    showError(errorMessage);
  }
}
</script>

<style scoped>
.settings-container {
  position: relative;
}

.alerts-container {
  position: fixed;
  top: 72px;
  left: 0;
  right: 0;
  z-index: 100;
  display: flex;
  flex-direction: column;
  align-items: center;
}

.alert-overlay {
  width: 80%;
  max-width: 600px;
}

.setting-item {
  padding: 16px;
}

.setting-content {
  display: flex;
  width: 100%;
  align-items: center;
}

.setting-info {
  flex: 2; /* 2/3 of space */
  padding-right: 16px;
}

.setting-value {
  flex: 1; /* 1/3 of space */
  display: flex;
  justify-content: flex-end;
}
</style> 