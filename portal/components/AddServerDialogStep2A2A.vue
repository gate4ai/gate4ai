<template>
  <div>
    <v-alert type="info" variant="tonal" class="mb-4" density="compact">
      Detected Server Protocol: <strong>A2A</strong>
    </v-alert>

    <v-text-field
      :model-value="serverName"
      label="Server Name *"
      placeholder="Enter a name for this server"
      required
      :rules="[rules.required]"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      data-testid="step2-server-name-input"
      @update:model-value="$emit('update:serverName', $event)"
    />
    <v-textarea
      :model-value="description"
      label="Description (Optional)"
      rows="2"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      @update:model-value="$emit('update:description', $event)"
    />
    <v-text-field
      :model-value="websiteUrl"
      label="Website URL (Optional)"
      hint="e.g. https://example.com"
      :rules="[rules.simpleUrl]"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      @update:model-value="$emit('update:websiteUrl', $event)"
    />
    <v-text-field
      :model-value="email"
      label="Contact Email (Optional)"
      hint="e.g. contact@example.com"
      :rules="[rules.email]"
      variant="outlined"
      density="compact"
      class="mb-4"
      :disabled="isLoading"
      @update:model-value="$emit('update:email', $event)"
    />
    <!-- Display discovered skills (read-only) -->
    <div v-if="a2aSkills && a2aSkills.length > 0">
      <h3 class="text-subtitle-1 mb-2">Agent Skills:</h3>
      <v-chip-group>
        <v-chip v-for="skill in a2aSkills" :key="skill.id" size="small">
          {{ skill.name }}
        </v-chip>
      </v-chip-group>
    </div>
    <v-alert v-else type="info" variant="text" density="compact" class="mt-2">
      No skills discovered for this A2A agent.
    </v-alert>

    <!-- Display Save Error Message -->
    <v-alert
      v-if="saveError && !isLoading"
      type="error"
      class="mt-4"
      density="compact"
    >
      {{ saveError }}
    </v-alert>
  </div>
</template>

<script setup lang="ts">
import { rules } from "~/utils/validation";

// Define the expected skill structure, allowing null for description
interface A2ASkillProp {
  id: string;
  name: string;
  description?: string | null; // Accept string, null, or undefined
  // Other fields like tags, examples, modes are not directly displayed here but could be added
}

// Props define the data passed from the parent and v-model bindings
defineProps<{
  serverName: string;
  description: string;
  websiteUrl: string;
  email: string;
  isLoading: boolean;
  a2aSkills: A2ASkillProp[]; // Use the refined interface
  saveError: string;
}>();

// Emits define events sent back to the parent for v-model updates
defineEmits<{
  (
    e:
      | "update:serverName"
      | "update:description"
      | "update:websiteUrl"
      | "update:email",
    value: string
  ): void;
}>();
</script>
