<template>
  <v-system-bar
    v-if="notificationMessage"
    app
    color="warning"
    class="global-notification-bar"
    height="30"
    data-testid="global-notification-bar"
  >
    <v-icon start size="small">mdi-information-outline</v-icon>
    <!-- Add data-testid to the span containing the text -->
    <span class="notification-text" data-testid="global-notification-text">{{
      notificationMessage
    }}</span>
  </v-system-bar>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useNuxtApp, useRuntimeConfig } from "#app"; // Import useRuntimeConfig again

const { $settings } = useNuxtApp();
const config = useRuntimeConfig(); // Use runtimeConfig again

// Get static message from environment variable via runtimeConfig
const staticMessage = computed(
  () => (config.public.gate4aiNotification as string) || ""
); // Restore reading from config

// Get dynamic message from settings plugin (remains the same)
const dynamicMessage = computed(() => {
  const settingValue = $settings.get("general_notification_dynamic");
  return typeof settingValue === "string" ? settingValue : "";
});

// Combine messages logic remains the same
const notificationMessage = computed(() => {
  const staticMsg = staticMessage.value.trim();
  const dynamicMsg = dynamicMessage.value.trim();

  if (staticMsg && dynamicMsg) {
    return `${staticMsg} | ${dynamicMsg}`;
  } else if (staticMsg) {
    return staticMsg;
  } else if (dynamicMsg) {
    return dynamicMsg;
  } else {
    return ""; // Return empty string if no messages
  }
});
</script>

<style scoped>
.global-notification-bar {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: 0.8rem;
  justify-content: center;
}

.notification-text {
  display: inline-block;
  vertical-align: middle;
  max-width: calc(100% - 30px);
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
