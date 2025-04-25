<template>
    <div>
      <div v-for="(item, index) in items" :key="index" class="d-flex align-center mb-2">
        <v-text-field
          :model-value="item.key"
          label="Key"
          variant="outlined"
          density="compact"
          hide-details
          class="mr-2 flex-grow-1"
          data-testid="key"
          :disabled="disabled"
          @update:model-value="updateItem(index, 'key', $event)"
        />
        <v-text-field
          :model-value="item.value"
          label="Value"
          variant="outlined"
          density="compact"
          hide-details
          class="mr-2 flex-grow-1"
          :disabled="disabled"
          data-testid="value"
          @update:model-value="updateItem(index, 'value', $event)"
        />
        <v-btn
          icon
          size="small"
          variant="text"
          color="error"
          :disabled="disabled"
          @click="removeItem(index)"
        >
          <v-icon>mdi-minus-circle</v-icon>
        </v-btn>
      </div>
      <v-btn
        variant="text"
        color="primary"
        prepend-icon="mdi-plus"
        :disabled="disabled"
        @click="addItem"
      >
        Add Header
      </v-btn>
    </div>
  </template>
  
  <script setup lang="ts">
  import { ref, watch, computed } from 'vue';
  
  type HeaderItem = { key: string; value: string };
  
  const props = defineProps<{
    modelValue: Record<string, string>; // Expecting { key: value, ... }
    disabled?: boolean;
  }>();
  
  const emit = defineEmits<{
    (e: 'update:modelValue', value: Record<string, string>): void;
  }>();
  
  // Internal representation as an array for easy v-for
  const items = ref<HeaderItem[]>([]);
  
  // Convert incoming object prop to internal array state
  const propToObjectArray = (obj: Record<string, string>): HeaderItem[] => {
    return Object.entries(obj || {}).map(([key, value]) => ({ key, value }));
  };
  
  // Convert internal array state back to object for emitting
  const arrayToPropObject = (arr: HeaderItem[]): Record<string, string> => {
    return arr.reduce((acc, item) => {
      if (item.key) { // Only include items with a key
        acc[item.key] = item.value;
      }
      return acc;
    }, {} as Record<string, string>);
  };
  
  // Watch the prop for external changes
  watch(() => props.modelValue, (newValue) => {
    // Avoid infinite loops by comparing stringified versions or using a deep comparison library
    if (JSON.stringify(newValue) !== JSON.stringify(arrayToPropObject(items.value))) {
      items.value = propToObjectArray(newValue);
    }
  }, { deep: true, immediate: true });
  
  
  function updateItem(index: number, field: 'key' | 'value', value: string) {
    if (items.value[index]) {
      items.value[index][field] = value;
      emitUpdate();
    }
  }
  
  function addItem() {
    items.value.push({ key: '', value: '' });
    // No need to emit here, will emit on key/value input
  }
  
  function removeItem(index: number) {
    items.value.splice(index, 1);
    emitUpdate();
  }
  
  // Emit the updated object whenever the internal array changes
  function emitUpdate() {
    emit('update:modelValue', arrayToPropObject(items.value));
  }
  
  </script>
  
  <style scoped>
  .flex-grow-1 {
    flex-grow: 1;
  }
  </style>