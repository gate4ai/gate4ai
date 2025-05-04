<template>
  <v-btn
    :color="buttonProps.color"
    data-testid="server-subscribe-button"
    :variant="buttonProps.variant"
    :loading="isLoading"
    :disabled="isDisabled"
    @click="handleClick"
  >
    <v-icon v-if="buttonProps.icon" start>{{ buttonProps.icon }}</v-icon>
    {{ buttonProps.text }}
  </v-btn>

  <!-- Keep dialog rendering logic here, controlled by composable state -->
  <SubscriptionHeaderValuesDialog
    v-if="showHeadersDialog"
    v-model="showHeadersDialog"
    :server-slug="server.slug"
    :subscription-id="subscriptionState.subscriptionId"
    @save="handleDialogSave"
  />
</template>

<script setup lang="ts">
import { computed, toRef } from "vue";
import type { ServerInfo } from "~/utils/server";
import { useSubscription } from "../composables/useSubscription"; // Import the composable
import SubscriptionHeaderValuesDialog from "./SubscriptionHeaderValuesDialog.vue";

const props = defineProps<{ server: ServerInfo; isAuthenticated: boolean }>();
const emit = defineEmits<{
  (
    e: "update:subscription",
    payload: {
      serverId: string;
      isSubscribed: boolean;
      subscriptionId?: string;
    }
  ): void;
}>();

// Use the composable
const serverRef = toRef(props, "server");
const isAuthenticatedRef = toRef(props, "isAuthenticated");
const {
  isLoading,
  showHeadersDialog,
  subscriptionState,
  isDisabled,
  subscribe,
  unsubscribe,
  handleHeadersSave, // Get the handler from composable
} = useSubscription(serverRef, isAuthenticatedRef);

// Button display logic based on composable state
const buttonProps = computed(() => {
  if (subscriptionState.isSubscribed) {
    return {
      text: "Unsubscribe",
      color: "error",
      icon: "mdi-account-minus",
      variant: "text" as const,
    };
  } else {
    return {
      text: "Subscribe",
      color: "primary",
      icon: "mdi-account-plus",
      variant: "elevated" as const,
    };
  }
});

// Handle button click using composable methods
async function handleClick() {
  let success = false;
  if (subscriptionState.isSubscribed) {
    success = await unsubscribe();
  } else {
    // subscribe() now handles the logic of checking template/showing dialog
    // We don't need to await its completion here, as the dialog handles the final step.
    await subscribe();
    // We don't know the final result here if the dialog opens.
    // The event emission will happen after handleDialogSave.
    return; // Exit early if subscribe() might open the dialog
  }

  // Emit update only if subscribe/unsubscribe finished directly AND succeeded
  if (success) {
    emit("update:subscription", {
      serverId: props.server.id,
      isSubscribed: subscriptionState.isSubscribed,
      subscriptionId: subscriptionState.subscriptionId,
    });
  }
}

// Handle the save event from the dialog using the composable's handler
async function handleDialogSave(headerValues: Record<string, string>) {
  const success = await handleHeadersSave(headerValues); // Call composable's method
  if (success) {
    // Emit update after successful subscription via dialog
    emit("update:subscription", {
      serverId: props.server.id,
      isSubscribed: subscriptionState.isSubscribed,
      subscriptionId: subscriptionState.subscriptionId,
    });
  }
}
</script>
