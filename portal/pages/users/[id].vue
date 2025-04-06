<template>
  <v-row justify="center">
    <v-col cols="12" md="8">
      <v-card class="pa-4">
        <v-card-title class="d-flex align-center">
          <span class="text-h4 mr-4">User Profile</span>
          <v-chip
            v-if="isAdminOrSecurity && user.role"
            color="primary"
          >
            {{ formatRole(user.role) }}
          </v-chip>
          <v-chip
            v-if="isAdminOrSecurity && user.status"
            :color="getStatusColor(user.status)"
            class="ml-2"
          >
            {{ formatStatus(user.status) }}
          </v-chip>
        </v-card-title>
        
        <v-card-text>
          <v-form ref="form" @submit.prevent="updateProfile">
            <v-text-field
              v-model="user.name"
              label="Full Name"
              required
              :rules="[rules.required]"
              variant="outlined"
              class="mb-4"
            />
            
            <v-text-field
              v-model="user.email"
              label="Email"
              type="email"
              required
              :rules="[rules.required, rules.email]"
              variant="outlined"
              class="mb-4"
              disabled
            />
            
            <v-text-field
              v-model="user.company"
              label="Company"
              variant="outlined"
              class="mb-4"
            />
            
            <!-- Admin-only fields -->
            <template v-if="isAdminOrSecurity">
              <v-divider class="my-4"/>
              <v-card-title class="text-h5 mb-2">Administration</v-card-title>
              
              <v-select
                v-model="user.role"
                label="Role"
                :items="roleOptions"
                variant="outlined"
                class="mb-4"
              />
              
              <v-select
                v-model="user.status"
                label="Status"
                :items="statusOptions"
                variant="outlined"
                class="mb-4"
              />
              
              <v-textarea
                v-model="user.comment"
                label="Comment"
                variant="outlined"
                class="mb-4"
                rows="3"
              />
            </template>
            
            <v-btn
              type="submit"
              color="primary"
              size="large"
              :loading="isLoading"
              class="mt-4"
            >
              Update Profile
            </v-btn>
          </v-form>
        </v-card-text>
      </v-card>
    </v-col>
  </v-row>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { rules } from '~/utils/validation';
import { useSnackbar } from '~/composables/useSnackbar';

definePageMeta({
  middleware: ['auth'],
  title: 'User Profile',
  layout: 'default',
});

const route = useRoute();
const userId = route.params.id;
const { $auth, $api } = useNuxtApp();

// Define a type for user data
interface UserData {
  id: string;
  name: string;
  email: string;
  company: string;
  role: string;
  status: string;
  comment: string;
}

interface UpdateUserData {
  name: string;
  company: string;
  role?: string;
  status?: string;
  comment?: string;
  [key: string]: unknown;
}

const user = ref<UserData>({
  id: '',
  name: '',
  email: '',
  company: '',
  role: '',
  status: '',
  comment: ''
});

const message = ref('');
const messageType = ref<'success' | 'error' | 'info' | 'warning'>('success');
const isLoading = ref(false);
const form = ref<{ validate: () => Promise<{ valid: boolean }> } | null>(null);

const { showError, showSuccess } = useSnackbar();

const roleOptions = [
  { title: 'None', value: 'EMPTY' },
  { title: 'User', value: 'USER' },
  { title: 'Developer', value: 'DEVELOPER' },
  { title: 'Admin', value: 'ADMIN' },
  { title: 'Security', value: 'SECURITY' }
];

const statusOptions = [
  { title: 'Active', value: 'ACTIVE' },
  { title: 'Email not confirmed', value: 'EMAIL_NOT_CONFIRMED' },
  { title: 'Blocked', value: 'BLOCKED' }
];

// Computed property to check if current user is admin or security
const isAdminOrSecurity = computed(() => {
  return $auth.isSecurityOrAdmin();
});

// Load user data on mount
onMounted(async () => {
  await fetchUserData();
});

async function fetchUserData() {
  try {
    isLoading.value = true;
    
    const userData = await $api.getJson(`/users/${userId}`) as UserData;
    user.value = userData;
  } catch (error: unknown) {
    console.error('Failed to load user data:', error);
    message.value = 'Failed to load user data.';
    messageType.value = 'error';
  } finally {
    isLoading.value = false;
  }
}

async function updateProfile() {
  if (!form.value) return;
  
  const { valid } = await form.value.validate();
  if (!valid) return;
  
  isLoading.value = true;
  message.value = '';
  
  try {
    // Prepare data to update
    const updateData: UpdateUserData = {
      name: user.value.name,
      company: user.value.company
    };
    
    // If admin, add admin-only fields
    if (isAdminOrSecurity.value) {
      updateData.role = user.value.role;
      updateData.status = user.value.status;
      updateData.comment = user.value.comment;
    }
    
    const updatedUser = await $api.putJson(`/users/${userId}`, updateData) as UserData;
    
    showSuccess('User updated successfully');
    
    // Update user data with the response
    user.value = updatedUser;
  } catch (error: unknown) {
    showError(error instanceof Error ? error.message : 'Failed to update user');
    console.error("Error updating user:", error);
  } finally {
    isLoading.value = false;
  }
}

function formatRole(role: string): string {
  switch (role) {
    case 'ADMIN': return 'Admin';
    case 'SECURITY': return 'Security';
    case 'USER': return 'User';
    case 'DEVELOPER': return 'Developer';
    case 'EMPTY':
    default: return 'User';
  }
}

function formatStatus(status: string): string {
  switch (status) {
    case 'ACTIVE': return 'Active';
    case 'BLOCKED': return 'Blocked';
    case 'EMAIL_NOT_CONFIRMED': return 'Email not confirmed';
    default: return status;
  }
}

function getStatusColor(status: string): string {
  switch (status) {
    case 'ACTIVE': return 'success';
    case 'BLOCKED': return 'error';
    case 'EMAIL_NOT_CONFIRMED': return 'warning';
    default: return 'info';
  }
}
</script> 