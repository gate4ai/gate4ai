<template>
  <v-row class="mb-6">
    <v-col cols="12" md="12">
      <v-text-field
        :model-value="modelValue"
        label="Search servers"
        prepend-inner-icon="mdi-magnify"
        variant="outlined"
        hide-details
        @update:model-value="$emit('update:modelValue', $event)"
      />
    </v-col>
  </v-row>
</template>

<script setup lang="ts">
defineProps<{
  modelValue: string;
}>();

defineEmits<{
  (e: 'update:modelValue', value: string): void;
}>();
</script> 