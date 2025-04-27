<template>
  <v-dialog v-model="dialog" max-width="500px">
    <v-card>
      <v-card-title class="text-h5">Delete Server</v-card-title>
      <v-card-text>
        Are you sure you want to delete this server? This action cannot be
        undone.
      </v-card-text>
      <v-card-actions>
        <v-spacer />
        <v-btn color="primary" variant="text" @click="closeDialog"
          >Cancel</v-btn
        >
        <v-btn color="error" variant="text" @click="$emit('confirm')"
          >Yes, Delete</v-btn
        >
      </v-card-actions>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { watch, ref } from "vue";

const props = defineProps<{
  modelValue: boolean;
}>();

const emit = defineEmits<{
  (e: "update:modelValue", value: boolean): void;
  (e: "confirm"): void;
}>();

const dialog = ref(props.modelValue);

watch(
  () => props.modelValue,
  (value) => {
    dialog.value = value;
  }
);

watch(dialog, (value) => {
  emit("update:modelValue", value);
});

function closeDialog() {
  dialog.value = false;
}
</script>
