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
    <span class="notification-text" data-testid="global-notification-text">{{
      notificationMessage
    }}</span>
  </v-system-bar>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useNuxtApp, useRuntimeConfig } from "#app"; // Import useRuntimeConfig again

const { $settings } = useNuxtApp();
const config = useRuntimeConfig();
const staticMsg = (config.public.gate4aiNotification as string) || "";

// Combine messages logic remains the same
const notificationMessage = computed(() => {
  const settingValue = $settings.get("general_notification_dynamic");
  const dynamicMsg = typeof settingValue === "string" ? settingValue : "";

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
