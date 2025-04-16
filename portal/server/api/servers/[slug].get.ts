import { defineEventHandler, getRouterParam, createError } from 'h3';
import prisma from '../../utils/prisma';
import type { User, SubscriptionStatus } from '@prisma/client'; // Adjusted imports
import { getServerReadAccessLevel, getSubscriptionStatusCounts } from '../../utils/serverPermissions'; // Import helper functions
import { mapDbA2ASkillToApiSkill, mapDbRestEndpointToApiEndpoint } from '../../utils/serverProtocols'; // Import mapping functions

export default defineEventHandler(async (event) => {
  const slug = getRouterParam(event, 'slug'); // Get slug instead of id
  // User might be undefined if not logged in
  const user = event.context.user as User | undefined;

  if (!slug) {
    throw createError({ statusCode: 400, statusMessage: 'Server slug is required' });
  }

  try {
    // 1. Fetch server data using SLUG, including necessary relations
    const server = await prisma.server.findUnique({
      where: { slug }, // Find by slug
      include: {
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
          orderBy: { name: 'asc' }
        },
        owners: { // Include owner user details
          select: {
            user: { // Select specific user fields
              select: {
                id: true,
                name: true,
                email: true, // Include email if needed for display
              }
            }
          },
        },
        _count: {
          select: {
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
    const { hasExtendedAccess, isOwner } = getServerReadAccessLevel(user, server);

    // 4. Check current user's subscription status and ID (only if user is logged in)
    let currentUserSubscriptionId: string | undefined = undefined;
    let isCurrentUserSubscribed = false;
    if (user) {
      const subscription = await prisma.subscription.findUnique({
        where: {
          userId_serverId: {
            userId: user.id,
            serverId: server.id, // Use the server's actual ID (UUID) for relation lookup
          },
        },
        select: { id: true, status: true },
      });
      if (subscription) {
          currentUserSubscriptionId = subscription.id;
          isCurrentUserSubscribed = subscription.status === 'ACTIVE';
      }
    }

    // 5. Fetch detailed subscription counts only if user has extended access
    let subscriptionStatusCounts: Record<SubscriptionStatus, number> | undefined = undefined;
    if (hasExtendedAccess) {
        subscriptionStatusCounts = await getSubscriptionStatusCounts(server.id); // Use ID here
    }

    // 6. Fetch protocol-specific data based on server.protocol
    let a2aSkills = undefined;
    let restEndpoints = undefined;

    // For A2A servers, fetch skills from the database
    if (server.protocol === 'A2A' && (hasExtendedAccess || isCurrentUserSubscribed)) {
      const skills = await prisma.a2ASkill.findMany({
        where: { serverId: server.id }
      });
      if (skills.length > 0) {
        a2aSkills = skills.map(mapDbA2ASkillToApiSkill);
      }
    }
    // For REST servers, fetch endpoints with their relations from the database
    else if (server.protocol === 'REST' && (hasExtendedAccess || isCurrentUserSubscribed)) {
      const endpoints = await prisma.rESTEndpoint.findMany({
        where: { serverId: server.id },
        include: {
          parameters: true,
          requestBody: true,
          responses: true
        }
      });
      if (endpoints.length > 0) {
        restEndpoints = endpoints.map(mapDbRestEndpointToApiEndpoint);
      }
    }

    // 7. Construct the response object based on permissions
    const responseData = {
      // --- Always Visible Fields ---
      id: server.id, // Still include ID
      slug: server.slug, // Include slug
      protocol: server.protocol, // Include protocol
      protocolVersion: server.protocolVersion, // Include protocol version
      name: server.name,
      description: server.description,
      website: server.website,
      email: server.email,
      imageUrl: server.imageUrl,
      createdAt: server.createdAt,
      updatedAt: server.updatedAt,
      isCurrentUserSubscribed: isCurrentUserSubscribed,
      isCurrentUserOwner: isOwner,
      subscriptionId: currentUserSubscriptionId,
      tools: server.tools.map(tool => ({
        id: tool.id,
        name: tool.name,
        description: tool.description,
        parameters: tool.parameters
      })),
      // Include selected owner info always
      owners: server.owners,
      _count: {
           tools: server.tools.length,
           subscriptions: server._count.subscriptions // Active count
      },

      // --- Extended Access Fields (Owner, Admin, Security) ---
      ...(hasExtendedAccess && {
        serverUrl: server.serverUrl,
        status: server.status,
        availability: server.availability,
        subscriptionStatusCounts: subscriptionStatusCounts,
      }),

      // --- Protocol-specific data ---
      ...(a2aSkills && { a2aSkills }),
      ...(restEndpoints && { restEndpoints }),
    };

    return responseData;

  } catch (error: unknown) {
    console.error(`Error fetching server with slug ${slug}:`, error);

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