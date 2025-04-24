import { computed } from 'vue';
import type { Ref } from 'vue';
// Change import to ServerInfo
import type { ServerInfo } from '~/utils/server';
import type { User } from '@prisma/client';

export function useSubscriptionPermissions(
    // Accept Ref<ServerInfo | null | undefined>
    serverInfo: Ref<ServerInfo | null | undefined>,
    user: Ref<User | null | undefined>
) {
  const canPerformAction = computed(() => {
    if (!user.value || !serverInfo.value) {
        return true; // Allow click for login redirect
    }

    const isAdminOrSecurity = user.value.role === 'ADMIN' || user.value.role === 'SECURITY';
    // Use the flag directly from ServerInfo
    const isOwner = serverInfo.value.isCurrentUserOwner ?? false;

    return !isAdminOrSecurity && !isOwner;
  });

  const getSubscriptionAlert = computed(() => {
    if (!user.value || !serverInfo.value) return null;

    const isAdminOrSecurity = user.value.role === 'ADMIN' || user.value.role === 'SECURITY';
    // Use the flag directly from ServerInfo
    const isOwner = serverInfo.value.isCurrentUserOwner ?? false;

    if (isAdminOrSecurity || isOwner) {
      return "Owners, Admins, and Security personnel have access by default and cannot subscribe/unsubscribe.";
    }
    return null;
  });

  return { canPerformAction, getSubscriptionAlert };
}