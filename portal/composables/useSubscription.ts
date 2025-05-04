import { ref, computed, reactive, watch, readonly } from "vue"; // Import readonly
import type { Ref } from "vue";
import { useRouter } from "vue-router";
import { useNuxtApp } from "#app";
import type { ServerInfo } from "~/utils/server";
import { useSubscriptionPermissions } from "./useSubscriptionPermissions";
import { useSnackbar } from "./useSnackbar";

// Interfaces (matching types used in components)
interface TemplateItem {
  id: string;
  key: string;
  description?: string | null;
  required: boolean;
}
interface SubscriptionResponse {
  id: string;
}
interface SubscriptionState {
  isSubscribed: boolean;
  subscriptionId?: string;
}

export function useSubscription(
  serverInfo: Ref<ServerInfo | null | undefined>,
  isAuthenticated: Ref<boolean>
) {
  const { $auth, $api } = useNuxtApp();
  const { showSuccess, showError } = useSnackbar();
  const router = useRouter();

  const isLoading = ref(false);
  const showHeadersDialog = ref(false);
  const headerTemplateCache = ref<TemplateItem[] | null>(null);
  const subscriptionState = reactive<SubscriptionState>({
    isSubscribed: serverInfo.value?.isCurrentUserSubscribed ?? false,
    subscriptionId: serverInfo.value?.subscriptionId,
  });

  // --- LOGGING ---
  console.log(
    `[useSubscription] Initializing state:`,
    `isSubscribed=${subscriptionState.isSubscribed}`,
    `subscriptionId=${subscriptionState.subscriptionId}`,
    `Based on serverInfo:`,
    serverInfo.value
  );

  // Watch for external changes to the server's subscription status
  watch(
    () => [
      serverInfo.value?.isCurrentUserSubscribed,
      serverInfo.value?.subscriptionId,
    ],
    (newValues, oldValues) => {
      // --- LOGGING ---
      console.log(
        `[useSubscription] Watcher triggered:`,
        `oldValues=(${oldValues?.[0]}, ${oldValues?.[1]})`,
        `newValues=(${newValues[0]}, ${newValues[1]})`,
        `currentState=(${subscriptionState.isSubscribed}, ${subscriptionState.subscriptionId})`
      );

      const newIsSubscribed = newValues[0];
      const newSubId = newValues[1];

      // Explicitly check types before assignment
      const resolvedIsSubscribed =
        typeof newIsSubscribed === "boolean" ? newIsSubscribed : false;
      const resolvedSubId = typeof newSubId === "string" ? newSubId : undefined;

      // Check if state actually changed before resetting cache
      const stateChanged =
        resolvedIsSubscribed !== subscriptionState.isSubscribed ||
        resolvedSubId !== subscriptionState.subscriptionId;

      // Update state
      subscriptionState.isSubscribed = resolvedIsSubscribed;
      subscriptionState.subscriptionId = resolvedSubId;

      // Reset template cache if subscription status changes externally
      if (stateChanged) {
        headerTemplateCache.value = null;
        console.log(`[useSubscription] State changed, template cache cleared.`);
      }

      // --- LOGGING ---
      console.log(
        `[useSubscription] State updated:`,
        `isSubscribed=${subscriptionState.isSubscribed}`,
        `subscriptionId=${subscriptionState.subscriptionId}`
      );
    },
    { deep: true } // Add deep watch just in case serverInfo object itself changes
  );

  const currentUser = computed(() => $auth.getUser());
  const { canPerformAction, getSubscriptionAlert } = useSubscriptionPermissions(
    serverInfo,
    currentUser
  );

  const isDisabled = computed(() => isLoading.value || !canPerformAction.value);

  // --- Core Subscription Logic ---

  async function performUnsubscribe(): Promise<boolean> {
    if (!serverInfo.value || !subscriptionState.subscriptionId) {
      showError("Cannot unsubscribe: Missing server info or subscription ID.");
      return false;
    }

    isLoading.value = true;
    try {
      // Use $api.deleteJson (Ensure this method exists in your $api plugin definition)
      await $api.deleteJson(
        `/subscriptions/${subscriptionState.subscriptionId}`
      );
      subscriptionState.isSubscribed = false;
      subscriptionState.subscriptionId = undefined;
      headerTemplateCache.value = null; // Clear cache on unsubscribe
      showSuccess("Unsubscribed!");
      return true;
    } catch (err: unknown) {
      // Use unknown instead of any
      const message =
        err instanceof Error ? err.message : "Unsubscription failed.";
      showError(message);
      console.error("Error unsubscribing:", err);
      return false;
    } finally {
      isLoading.value = false;
    }
  }

  async function checkTemplateAndSubscribe() {
    if (!serverInfo.value) {
      showError("Cannot subscribe: Server information is missing.");
      return;
    }
    isLoading.value = true;
    try {
      // Fetch template if not cached
      if (headerTemplateCache.value === null) {
        headerTemplateCache.value = await $api.getJson<TemplateItem[]>(
          `/servers/${serverInfo.value.slug}/subscription-header-template`
        );
      }
      // If template has items, show dialog
      if (headerTemplateCache.value && headerTemplateCache.value.length > 0) {
        showHeadersDialog.value = true;
        // Loading stops here, dialog interaction takes over
      } else {
        // No template, proceed directly
        await performSubscribe();
      }
    } catch (err: unknown) {
      // Use unknown instead of any
      const message =
        err instanceof Error
          ? err.message
          : "Failed to check subscription requirements.";
      showError(message);
      console.error("Error checking template:", err);
      isLoading.value = false; // Stop loading on error
    } finally {
      // Don't stop loading if dialog is shown
      if (!showHeadersDialog.value) {
        isLoading.value = false;
      }
    }
  }

  // Called after header dialog save OR directly if no template
  async function performSubscribe(
    headerValues?: Record<string, string>
  ): Promise<boolean> {
    if (!serverInfo.value) {
      showError("Cannot subscribe: Server information is missing.");
      isLoading.value = false; // Ensure loading stops
      return false;
    }
    isLoading.value = true; // Ensure loading is active
    try {
      const payload: {
        serverId: string;
        headerValues?: Record<string, string>;
      } = { serverId: serverInfo.value.id };
      if (headerValues && Object.keys(headerValues).length > 0) {
        payload.headerValues = headerValues;
      }

      const newSubscription = await $api.postJson<SubscriptionResponse>(
        "/subscriptions",
        payload
      );
      subscriptionState.isSubscribed = true;
      subscriptionState.subscriptionId = newSubscription.id;
      showSuccess("Subscribed!");
      return true;
    } catch (err: unknown) {
      // Use unknown instead of any
      const message =
        err instanceof Error ? err.message : "Subscription failed.";
      showError(message);
      console.error("Error subscribing:", err);
      return false;
    } finally {
      isLoading.value = false;
    }
  }

  // --- Public Methods ---

  async function subscribe(): Promise<boolean> {
    if (!isAuthenticated.value) {
      router.push(`/login?redirect=/servers/${serverInfo.value?.slug ?? ""}`);
      return false;
    }
    if (!canPerformAction.value) {
      const msg = getSubscriptionAlert.value;
      if (msg) showError(msg);
      return false;
    }
    // Check template and potentially open dialog or call performSubscribe
    await checkTemplateAndSubscribe();
    // The actual success depends on the subsequent steps (dialog or direct subscribe)
    // We return true optimistically if the process starts, false otherwise
    // Return based on isLoading being false (meaning process finished or dialog opened)
    // And also check if an error occurred during checkTemplateAndSubscribe which stops loading early
    return !isLoading.value && !showError; // Approximation: true if dialog opened or direct subscribe attempted.
  }

  async function unsubscribe(): Promise<boolean> {
    if (!isAuthenticated.value) {
      router.push(`/login?redirect=/servers/${serverInfo.value?.slug ?? ""}`);
      return false;
    }
    if (!canPerformAction.value) {
      const msg = getSubscriptionAlert.value;
      if (msg) showError(msg);
      return false;
    }
    return await performUnsubscribe();
  }

  // Method to handle saving from the headers dialog
  async function handleHeadersSave(
    headerValues: Record<string, string>
  ): Promise<boolean> {
    showHeadersDialog.value = false; // Close dialog first
    return await performSubscribe(headerValues); // Then perform subscription
  }

  return {
    isLoading: readonly(isLoading),
    showHeadersDialog, // Make this writable for the dialog component v-model
    subscriptionState, // Expose reactive state
    isDisabled: readonly(isDisabled),
    subscribe,
    unsubscribe,
    handleHeadersSave, // Expose this for the dialog interaction
  };
}
