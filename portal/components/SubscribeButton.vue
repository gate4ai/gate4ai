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

  <SubscriptionHeaderValuesDialog
    v-if="showHeadersDialog"
    v-model="showHeadersDialog"
    :server-slug="server.slug"
    @save="handleHeadersSave"
  />
</template>

<script setup lang="ts">
import { ref, computed, reactive, watch } from 'vue';
import { useRouter } from 'vue-router';
import type { ServerInfo } from '~/utils/server';
import { useSubscriptionPermissions } from '../composables/useSubscriptionPermissions';
import { useSnackbar } from '../composables/useSnackbar';
import SubscriptionHeaderValuesDialog from './SubscriptionHeaderValuesDialog.vue';

interface TemplateItem { id: string; key: string; description?: string | null; required: boolean; }
interface SubscriptionResponse { id: string; }

const props = defineProps<{ server: ServerInfo; isAuthenticated: boolean; }>();
const emit = defineEmits<{(e: 'update:subscription', payload: { serverId: string; isSubscribed: boolean; subscriptionId?: string }): void;}>();
const router = useRouter();
const { $auth, $api } = useNuxtApp();
const { showSuccess, showError } = useSnackbar();
const isLoading = ref(false);
const showHeadersDialog = ref(false);
const headerTemplateCache = ref<TemplateItem[] | null>(null);
const localSubscriptionState = reactive({ isSubscribed: props.server.isCurrentUserSubscribed ?? false, subscriptionId: props.server.subscriptionId });
const currentUser = computed(() => $auth.getUser());
const serverRef = computed(() => props.server);
const { canPerformAction, getSubscriptionAlert } = useSubscriptionPermissions(serverRef, currentUser);
const isDisabled = computed(() => isLoading.value || !canPerformAction.value);

const buttonProps = computed(() => { /* ... */
  if (localSubscriptionState.isSubscribed) { return { text: 'Unsubscribe', color: 'error', icon: 'mdi-account-minus', variant: 'text' as const }; }
  else { return { text: 'Subscribe', color: 'primary', icon: 'mdi-account-plus', variant: 'elevated' as const }; }
});

async function handleClick() { /* ... */
 if (!props.isAuthenticated) { router.push(`/login?redirect=/servers/${props.server.slug}`); return; }
  if (!canPerformAction.value) { const msg = getSubscriptionAlert.value; if (msg) showError(msg); return; }
  if (localSubscriptionState.isSubscribed) { await performUnsubscribe(); }
  else { await checkTemplateAndSubscribe(); }
}
async function performUnsubscribe() { /* ... */
 isLoading.value = true; try { if (!localSubscriptionState.subscriptionId) throw new Error("Subscription ID missing."); await $api.deleteJson(`/subscriptions/${localSubscriptionState.subscriptionId}`); localSubscriptionState.isSubscribed = false; localSubscriptionState.subscriptionId = undefined; showSuccess('Unsubscribed!'); emit('update:subscription', { serverId: props.server.id, isSubscribed: false }); } catch (err: unknown) { const message = err instanceof Error ? err.message : 'Unsubscription failed.'; showError(message); console.error("Error unsubscribing:", err); } finally { isLoading.value = false; }
}
async function checkTemplateAndSubscribe() { /* ... */
 isLoading.value = true; try { if (headerTemplateCache.value === null) { headerTemplateCache.value = await $api.getJson<TemplateItem[]>(`/servers/${props.server.slug}/subscription-header-template`); } if (headerTemplateCache.value?.length > 0) { showHeadersDialog.value = true; } else { await performSubscribe(); } } catch (err) { const message = err instanceof Error ? err.message : 'Failed to check requirements.'; showError(message); console.error("Error checking template:", err); } finally { if (!showHeadersDialog.value) isLoading.value = false; }
}
async function handleHeadersSave(headerValues: Record<string, string>) { /* ... */
 showHeadersDialog.value = false; await performSubscribe(headerValues);
}
async function performSubscribe(headerValues?: Record<string, string>) { /* ... */
 isLoading.value = true; try { const payload: { serverId: string; headerValues?: Record<string, string> } = { serverId: props.server.id }; if (headerValues) payload.headerValues = headerValues; const newSubscription = await $api.postJson<SubscriptionResponse>('/subscriptions', payload); localSubscriptionState.isSubscribed = true; localSubscriptionState.subscriptionId = newSubscription.id; showSuccess('Subscribed!'); emit('update:subscription', { serverId: props.server.id, isSubscribed: true, subscriptionId: newSubscription.id }); } catch (err: unknown) { const message = err instanceof Error ? err.message : 'Subscription failed.'; showError(message); console.error("Error subscribing:", err); } finally { isLoading.value = false; }
}

// Watch for prop changes - Explicitly type the destructured values
watch(() => [props.server.isCurrentUserSubscribed, props.server.subscriptionId],
    ([newIsSubscribed, newSubId]: [boolean | undefined, string | undefined]) => {
    // Now TS knows the types correctly
    localSubscriptionState.isSubscribed = newIsSubscribed ?? false;
    localSubscriptionState.subscriptionId = newSubId ?? undefined;
});

</script>