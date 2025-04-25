import { defineEventHandler, getRouterParam, createError } from 'h3';
import prisma from '../../../utils/prisma';
import { checkAuth } from '../../../utils/userUtils';
import { getServerReadAccessLevel } from '../../../utils/serverPermissions'; // Import helper
import type { Server } from '@prisma/client';

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated
  const serverSlug = getRouterParam(event, 'slug'); // Get slug from route

  if (!serverSlug) {
    throw createError({ statusCode: 400, statusMessage: 'Server slug is required' });
  }

  try {
    // 1. Find the server by slug to get its ID and check permissions
    const server = await prisma.server.findUnique({
      where: { slug: serverSlug },
      select: {
        id: true, // Need the actual server ID (UUID)
        owners: { select: { user: { select: { id: true } } } } // Need owners for permission check
      }
    });

    if (!server) {
      throw createError({ statusCode: 404, statusMessage: 'Server not found' });
    }

    // 2. Check if the current user has permission to view subscriptions
    // Type assertion needed as Prisma select doesn't fully type nested relations easily
    const serverWithOwnerUsers = server as unknown as Server & { owners: { user: { id: string } }[] };
    const { isOwner, isAdminOrSecurity } = getServerReadAccessLevel(user, serverWithOwnerUsers);

    if (!isOwner && !isAdminOrSecurity) {
      throw createError({ statusCode: 403, statusMessage: 'Forbidden: You do not have permission to view these subscriptions.' });
    }

    // 3. Fetch subscriptions using the server's actual ID (UUID)
    const subscriptions = await prisma.subscription.findMany({
      where: {
        serverId: server.id // Use the fetched server ID
      },
      include: {
        // Include user details based on settings/permissions if needed later
        user: {
          select: {
            id: true,
            name: true,
            email: true // Select email, frontend logic will decide to show it
          }
        }
      },
      orderBy: {
        createdAt: 'desc' // Or order as needed
      }
    });

    return subscriptions;

  } catch (error: unknown) {
    console.error(`Error fetching subscriptions for server slug ${serverSlug}:`, error);
    if (error instanceof Error && 'statusCode' in error) {
      throw error;
    }
    throw createError({ statusCode: 500, statusMessage: 'Failed to fetch subscriptions' });
  }
});