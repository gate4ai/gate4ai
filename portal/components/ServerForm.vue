<template>
  <v-form ref="form" @submit.prevent="submitForm">
    <v-row>
      <v-col cols="12">
        <v-text-field
          v-model="localServerData.name"
          label="Server Name"
          required
          :rules="[rules.required]"
        />
      </v-col>

      <v-col cols="12">
        <v-textarea
          v-model="localServerData.description"
          label="Description"
          rows="3"
        />
      </v-col>

      <v-col cols="12" md="6">
        <v-text-field
          v-model="localServerData.website"
          label="Website URL"
          hint="e.g. https://example.com"
          :rules="[rules.simpleUrl]"
        />
      </v-col>

      <v-col cols="12" md="6">
        <v-text-field
          v-model="localServerData.email"
          label="Contact Email"
          hint="e.g. contact@example.com"
          :rules="[rules.email]"
        />
      </v-col>

      <v-col cols="12" md="6">
        <v-text-field
          v-model="localServerData.imageUrl"
          label="Image URL"
          hint="URL to server image"
          :rules="[rules.simpleUrl]"
        />
      </v-col>

      <v-col cols="12" md="6">
        <v-text-field
          v-model="localServerData.serverUrl"
          label="Server URL"
          required
          hint="URL for API requests"
          :rules="serverUrlRules"
        />
      </v-col>

      <v-col cols="12" md="6">
        <v-select
          v-model="localServerData.status"
          :items="statusOptions"
          label="Status"
          required
        />
      </v-col>

      <v-col cols="12" md="6">
        <v-select
          v-model="localServerData.availability"
          :items="availabilityOptions"
          label="Availability"
          required
        />
      </v-col>
    </v-row>

    <v-card-actions>
      <v-spacer />
      <v-btn variant="outlined" @click="$emit('cancel')">Cancel</v-btn>
      <v-btn 
        color="primary" 
        type="submit"
        :loading="isSubmitting"
      >
        {{ submitLabel }}
      </v-btn>
    </v-card-actions>
  </v-form>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue';
import { rules } from '~/utils/validation';
import type { ServerData } from '~/utils/server';

// Extended ServerData interface with additional properties needed for the form
interface ServerFormData extends ServerData {
  id?: string;
  serverUrl: string;
  status: string;
  availability: string;
}

const props = defineProps<{
  serverData: ServerFormData;
  isSubmitting?: boolean;
  submitLabel?: string;
}>();

const emit = defineEmits<{
  submit: [updatedData: ServerFormData];
  cancel: [];
}>();

const form = ref<HTMLFormElement | null>(null);
const localServerData = ref({ ...props.serverData });

// Watch for changes in the prop and update local data
watch(() => props.serverData, (newVal) => {
  localServerData.value = { ...newVal };
}, { deep: true });

// Function to emit submit event with updated data
function submitForm() {
  emit('submit', localServerData.value);
}

// Server URL validation rules
const serverUrlRules = [
  rules.required,
  rules.url
];

// Options for select fields
const statusOptions = [
  { title: 'Draft', value: 'DRAFT' },
  { title: 'Active', value: 'ACTIVE' },
  { title: 'Blocked', value: 'BLOCKED' }
];

const availabilityOptions = [
  { title: 'Public', value: 'PUBLIC' },
  { title: 'Private', value: 'PRIVATE' },
  { title: 'Subscription', value: 'SUBSCRIPTION' }
];
</script>