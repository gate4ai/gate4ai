<template>
  <v-btn
    id="add-server-button"
    data-testid="add-server-button"
    color="primary"
    prepend-icon="mdi-plus"
    :disabled="disabled"
    :loading="loading"
    @click="handleClick"
  >
    Add Server
  </v-btn>
</template>

<script setup lang="ts">
import { useRouter, useRoute } from "vue-router";

const props = defineProps<{
  isAuthenticated: boolean;
  disabled?: boolean; // Optional prop to disable button from parent
  loading?: boolean; // Optional prop for loading state from parent
}>();

const emit = defineEmits<{
  // Emit an event for the parent to handle when the user is authenticated
  (e: "open-add-dialog"): void;
}>();

const router = useRouter();
const route = useRoute();

function handleClick() {
  if (props.isAuthenticated) {
    // If authenticated, let the parent page handle opening the dialog
    emit("open-add-dialog");
  } else {
    // If not authenticated, redirect to login, saving the current path
    const redirectPath = route.fullPath || "/servers"; // Fallback to /servers
    router.push(`/login?redirect=${encodeURIComponent(redirectPath)}`);
  }
}
</script>
