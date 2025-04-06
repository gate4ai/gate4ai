// /home/alex/go-ai/gate4ai/www/composables/useSubscriptionPermissions.ts
import { computed } from 'vue';
import type { Ref } from 'vue';
import type { Server } from '~/utils/server';
import type { User } from '@prisma/client'; // Assuming User type is globally available or imported

export function useSubscriptionPermissions(
    server: Ref<Server | null | undefined>,
    user: Ref<User | null | undefined>
) {
  const canPerformAction = computed(() => {
    // User must be logged in to perform actions, but button might show for guests (leading to login)
    // The actual check is whether they are NOT owner/admin/security
    if (!user.value || !server.value) {
        // If user not logged in, or server data missing, assume they *could* subscribe if logged in
        return true; // Allows button click to trigger login redirect or handle missing data
    }

    const isAdminOrSecurity = user.value.role === 'ADMIN' || user.value.role === 'SECURITY';
    // Ensure owners array exists before checking
    const isOwner = server.value.owners?.some(owner => owner.id === user.value!.id) ?? false;

    // Can perform subscribe/unsubscribe action only if NOT admin/security/owner
    return !isAdminOrSecurity && !isOwner;
  });

  const getSubscriptionAlert = computed(() => {
    if (!user.value || !server.value) return null; // No alert if not logged in or no server

    const isAdminOrSecurity = user.value.role === 'ADMIN' || user.value.role === 'SECURITY';
    const isOwner = server.value.owners?.some(owner => owner.id === user.value!.id) ?? false;

    if (isAdminOrSecurity || isOwner) {
      return "Owners, Admins, and Security personnel have access by default and cannot subscribe/unsubscribe.";
    }
    return null; // No alert needed for regular users
  });

  // Renamed canSubscribe to canPerformAction as it applies to both sub/unsub
  return { canPerformAction, getSubscriptionAlert };
}