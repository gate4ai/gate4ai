// /home/alex/go-ai/gate4ai/www/server/api/subscriptions/[id].delete.ts
// Handles DELETE /api/subscriptions/{subscriptionId} (Unsubscribe)
import { defineEventHandler, getRouterParam, createError } from 'h3';
import prisma from '../../utils/prisma';
import { checkAuth } from '../../utils/userUtils';
import { getServerReadAccessLevel as _getServerReadAccessLevel } from '../../utils/serverPermissions'; // Re-use if needed

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated
  const subscriptionId = getRouterParam(event, 'id');

  if (!subscriptionId) {
    throw createError({ statusCode: 400, statusMessage: 'Subscription ID is required' });
  }

  try {
    // 1. Find the subscription to verify ownership or admin rights
    const subscription = await prisma.subscription.findUnique({
      where: { id: subscriptionId },
      select: {
          userId: true,
          serverId: true, // Needed if admins/owners can also delete
          // Include server owners if needed for permission check
          // server: { select: { owners: { select: { id: true } } } }
       }
    });

    if (!subscription) {
      throw createError({ statusCode: 404, statusMessage: 'Subscription not found' });
    }

    // 2. Permission Check: Only the user themselves can unsubscribe
    // (Admins/Owners usually manage via status, not deletion, unless cleanup)
    if (subscription.userId !== user.id) {
       // Optional: Allow Admins/Security/Server Owners to delete any subscription?
       // const { isAdminOrSecurity } = getServerReadAccessLevel(user, subscription.server); // Fetch server above if needed
       // const isServerOwner = subscription.server?.owners.some(o => o.id === user.id);
       // if (!isAdminOrSecurity && !isServerOwner) { // If allowing admins/owners
            throw createError({ statusCode: 403, statusMessage: 'Forbidden: You can only unsubscribe yourself.' });
       // }
    }

    // 3. Delete the subscription
    await prisma.subscription.delete({
      where: { id: subscriptionId },
    });

    event.node.res.statusCode = 204; // No Content
    return; // Return nothing

  } catch (error: unknown) {
    console.error(`Error deleting subscription ${subscriptionId}:`, error);
    if (error instanceof Error && 'statusCode' in error) {
      throw error;
    }
    // Handle Prisma error if record not found during delete (already handled by findUnique)
    throw createError({ statusCode: 500, statusMessage: 'Failed to unsubscribe' });
  }
});