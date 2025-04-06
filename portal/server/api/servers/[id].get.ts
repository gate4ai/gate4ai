//////////////////////////
// /home/alex/go-ai/gate4ai/www/server/api/servers/[id].get.ts
//////////////////////////
import { defineEventHandler, getRouterParam, createError } from 'h3';
import prisma from '../../utils/prisma';
import type { User, SubscriptionStatus, Server as PrismaServer } from '@prisma/client'; // Remove ServerOwner import
import { getServerReadAccessLevel, getSubscriptionStatusCounts } from '../../utils/serverPermissions'; // Import helper functions

export default defineEventHandler(async (event) => {
  const id = getRouterParam(event, 'id');
  // User might be undefined if not logged in
  const user = event.context.user as User | undefined;

  if (!id) {
    throw createError({ statusCode: 400, statusMessage: 'Server ID is required' });
  }

  try {
    // 1. Fetch server data including necessary relations
    const server = await prisma.server.findUnique({
      where: { id },
      include: {
        // Include tools and their parameters always
        tools: {
          include: {
            parameters: {
              select: {
                id: true,
                name: true,
                type: true,
                description: true,
                required: true,
              }
            },
          },
          orderBy: { name: 'asc' } // Optional: Order tools
        },
        owners: {
          select: {
            user: true,
          },
        },
        // Count active subscribers
        _count: {
          select: {
            // tools: true, // Already have tools array, count might be redundant unless specifically needed
            subscriptions: { where: { status: 'ACTIVE' } },
          },
        },
      },
    });

    // 2. Check if server exists
    if (!server) {
      throw createError({ statusCode: 404, statusMessage: 'Server not found' });
    }

    // 3. Determine user's read access level using the helper
    // Pass the fetched server with owners to the helper
    const { hasExtendedAccess, isOwner } = getServerReadAccessLevel(user, server as PrismaServer & { owners: User[] });

    // 4. Check current user's subscription status and ID (only if user is logged in)
    let currentUserSubscriptionId: string | undefined = undefined;
    let isCurrentUserSubscribed = false;
    if (user) {
      const subscription = await prisma.subscription.findUnique({
        where: {
          userId_serverId: { // Use the compound unique index
            userId: user.id,
            serverId: id,
          },
        },
        select: { id: true, status: true }, // Select ID and status
      });
      if (subscription) {
          currentUserSubscriptionId = subscription.id;
          isCurrentUserSubscribed = subscription.status === 'ACTIVE';
      }
    }

    // 5. Fetch detailed subscription counts only if user has extended access
    let subscriptionStatusCounts: Record<SubscriptionStatus, number> | undefined = undefined;
    if (hasExtendedAccess) {
        subscriptionStatusCounts = await getSubscriptionStatusCounts(id);
    }

    // 6. Construct the response object based on permissions
    const responseData = {
      // --- Always Visible Fields ---
      id: server.id,
      name: server.name,
      description: server.description,
      website: server.website,
      email: server.email, // Public contact email
      imageUrl: server.imageUrl,
      createdAt: server.createdAt,
      updatedAt: server.updatedAt,
      // subscriberCount: server._count.subscriptions, // Active subscriber count
      isCurrentUserSubscribed: isCurrentUserSubscribed, // Flag for the logged-in user
      isCurrentUserOwner: isOwner, // Flag for the logged-in user
      subscriptionId: currentUserSubscriptionId, // Pass the subscription ID
      tools: server.tools.map(tool => ({
        id: tool.id,
        name: tool.name,
        description: tool.description,
        parameters: tool.parameters // Parameters are already fetched correctly
      })),
      // Include owner info always now
      owners: server.owners,

      // --- Extended Access Fields (Owner, Admin, Security) ---
      ...(hasExtendedAccess && {
        serverUrl: server.serverUrl,
        status: server.status,
        availability: server.availability,
        subscriptionStatusCounts: subscriptionStatusCounts, // Grouped counts by status
        // Add other sensitive fields here if any
      }),
      // Add counts separately if needed, otherwise derive from tools length and subscriptionStatusCounts
       _count: {
            tools: server.tools.length,
            subscriptions: server._count.subscriptions // Active count
       }
    };

    return responseData;

  } catch (error: unknown) {
    console.error(`Error fetching server ${id}:`, error);

    // Re-throw errors with status codes (like 404 Not Found)
    if (error instanceof Error && 'statusCode' in error) {
      throw error;
    }

    // Generic fallback error
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to fetch server due to an unexpected error.',
    });
  }
});