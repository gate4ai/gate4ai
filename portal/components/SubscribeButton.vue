// /home/alex/go-ai/gate4ai/www/components/SubscribeButton.vue
<template>
  <v-btn
    :color="buttonProps.color"
    :variant="buttonProps.variant"
    :loading="isLoading"
    :disabled="isDisabled"
    @click="handleClick"
  >
    <v-icon v-if="buttonProps.icon" start>{{ buttonProps.icon }}</v-icon>
    {{ buttonProps.text }}
  </v-btn>
</template>

<script setup lang="ts">
import { ref, computed, reactive } from 'vue';
import { useRouter } from 'vue-router';
import type { ServerInfo } from '~/utils/server'; 
import { useSubscriptionPermissions } from '../composables/useSubscriptionPermissions';
import { useSnackbar } from '../composables/useSnackbar'; 

const props = defineProps<{
  server: ServerInfo; // Expect server object with id, isCurrentUserSubscribed, subscriptionId?, owners?
  isAuthenticated: boolean;
}>();

// Emit events to notify parent about state change - useful for list views
const emit = defineEmits<{
  (e: 'update:subscription', payload: { serverId: string; isSubscribed: boolean; subscriptionId?: string }): void;
}>();


const router = useRouter();
const { $auth, $api } = useNuxtApp();
const { showSuccess, showError } = useSnackbar(); // Use snackbar

// --- State ---
const isLoading = ref(false);

const localSubscriptionState = reactive({
    isSubscribed: props.server.isCurrentUserSubscribed ?? false,
    subscriptionId: props.server.subscriptionId
});

// --- Permissions ---
const currentUser = computed(() => $auth.getUser());
// Pass reactive refs to the composable
const serverRef = computed(() => props.server);
const { canPerformAction, getSubscriptionAlert } = useSubscriptionPermissions(serverRef, currentUser);

const isDisabled = computed(() => {
    return isLoading.value || !canPerformAction.value;
});

// --- Button Appearance ---
const buttonProps = computed(() => {
  if (localSubscriptionState.isSubscribed) {
    return { text: 'Unsubscribe', color: 'error', icon: 'mdi-account-minus', variant: 'text' as const }; // Use 'text' variant for unsubscribe
  } else {
    return { text: 'Subscribe', color: 'primary', icon: 'mdi-account-plus', variant: 'elevated' as const }; // Use 'elevated' or 'tonal'
  }
});

// --- Actions ---
async function handleClick() {
  if (!props.isAuthenticated) {
    router.push(`/login?redirect=/servers/${props.server.id}`);
    return;
  }

  // Double check permissions before action (though button should be disabled)
  if (!canPerformAction.value) {
    const alertMsg = getSubscriptionAlert.value; // Эта часть должна быть в порядке
    if (alertMsg) {
        showError(alertMsg);
    }
    return;
  }

  isLoading.value = true;

  try {
    if (localSubscriptionState.isSubscribed) {
      // --- Unsubscribe ---
      if (!localSubscriptionState.subscriptionId) {
        throw new Error("Cannot unsubscribe: Subscription ID is missing.");
      }
      await $api.deleteJson(`/subscriptions/${localSubscriptionState.subscriptionId}`);
      localSubscriptionState.isSubscribed = false;
      localSubscriptionState.subscriptionId = undefined;
      showSuccess('Successfully unsubscribed!');
      // Emit update event
       emit('update:subscription', { serverId: props.server.id, isSubscribed: false });

    } else {
      // --- Subscribe ---
      const newSubscription = await $api.postJson('/subscriptions', { serverId: props.server.id });
      localSubscriptionState.isSubscribed = true;
      localSubscriptionState.subscriptionId = newSubscription.id; // Assuming API returns the created sub with ID
      showSuccess('Successfully subscribed!');
       // Emit update event
       emit('update:subscription', { serverId: props.server.id, isSubscribed: true, subscriptionId: newSubscription.id });
    }
  } catch (err: unknown) {
    if (err instanceof Error) {
      showError(err.message);
    } else {
      showError('Subscription action failed. Please try again.');
    }
    console.error("Error handling subscription:", err);
  } finally {
    isLoading.value = false;
  }
}

// Watch for prop changes to update local state if parent refreshes data
watch(() => [props.server.isCurrentUserSubscribed, props.server.subscriptionId], ([newIsSubscribed, newSubId]) => {
    localSubscriptionState.isSubscribed = newIsSubscribed ?? false;
    localSubscriptionState.subscriptionId = newSubId;
});

</script>