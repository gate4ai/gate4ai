// /home/alex/go-ai/gate4ai/www/server/utils/serverPermissions.ts
import type { H3Event } from 'h3';
import { createError } from 'h3';
import prisma from './prisma';
import type { User, Server, Role as _Role, SubscriptionStatus } from '@prisma/client'; // Added underscore prefix
import { checkAuth } from './userUtils';

/**
 * Checks if the authenticated user has ownership or administrative/security privileges
 * for modifying or deleting a specific server.
 * Throws an error if the user is not authorized.
 * Reuses fetched server data to avoid extra DB calls in the handler.
 *
 * @param event The H3 event object containing the user context.
 * @param serverId The ID of the server to check permissions for.
 * @returns {Promise<{ user: User, server: Server & { owners: {id: string}[] } , isOwner: boolean, isAdminOrSecurity: boolean }>} Details if authorized, including the fetched server.
 * @throws {Error} 401 if not authenticated, 404 if server not found, 403 if forbidden.
 */
export async function checkServerModificationRights(event: H3Event, serverId: string) {
  const user = checkAuth(event); // Ensure user is authenticated

  const server = await prisma.server.findUnique({
    where: { id: serverId },
    include: {
      owners: {
        select: { userId: true },
      },
    },
  });

  if (!server) {
    throw createError({ statusCode: 404, statusMessage: 'Server not found' });
  }

  const isOwner = server.owners.some(owner => owner.userId === user.id);
  const isAdminOrSecurity = user.role === 'ADMIN' || user.role === 'SECURITY';

  // Only Owners, Admins, or Security can modify/delete
  if (!isOwner && !isAdminOrSecurity) {
    throw createError({
      statusCode: 403,
      statusMessage: 'Forbidden: You do not have permission to modify or delete this server.',
    });
  }

  // Return useful info for the handler, including the already fetched server
  return { user, server, isOwner, isAdminOrSecurity };
}

/**
 * Checks if the authenticated user has permission to *create* a server.
 * Depends on user role and the 'only_developer_can_post_server' setting.
 * Throws an error if the user is not authorized.
 *
 * @param event The H3 event object containing the user context.
 * @returns {Promise<{ user: User }>} The authenticated user if authorized.
 * @throws {Error} 401 if not authenticated, 403 if forbidden.
 */
export async function checkServerCreationRights(event: H3Event) {
    const user = checkAuth(event); // Ensure user is authenticated

    // Admins and Security can always create
    if (user.role === 'ADMIN' || user.role === 'SECURITY') {
        return { user };
    }

    // Developers can always create
    if (user.role === 'DEVELOPER') {
        return { user };
    }

    // Regular users depend on the setting
    if (user.role === 'USER') {
        let onlyDevsCanPost = false; // Default to false (permissive)
        try {
            const setting = await prisma.settings.findUnique({
                where: { key: 'only_developer_can_post_server' },
                select: { value: true }
            });
            // Ensure setting exists and its value is explicitly true (assuming JSON boolean)
            if (setting && typeof setting.value === 'boolean' && setting.value === true) {
                onlyDevsCanPost = true;
            }
        } catch (e) {
            console.error("Error fetching 'only_developer_can_post_server' setting:", e);
            // Proceed with default permissive behavior in case of error? Or deny?
            // Let's be permissive for now, assuming default is anyone can post.
        }


        if (onlyDevsCanPost) {
             throw createError({
                statusCode: 403,
                statusMessage: 'Forbidden: Only developers, admins, or security personnel can create servers.',
            });
        }
        // If setting is false or not found, USER is allowed
        return { user };
    }

    // Should not happen if roles are exhaustive, but catch just in case
    throw createError({
        statusCode: 403,
        statusMessage: 'Forbidden: Insufficient role to create a server.',
    });
}

// --- Added Helper Function for Read Permissions ---
/**
 * Determines the access level for reading server details.
 * Does not throw errors, simply returns the level.
 *
 * @param user The authenticated user (or undefined).
 * @param server The server object including owners list.
 * @returns {{ hasExtendedAccess: boolean, isOwner: boolean, isAdminOrSecurity: boolean }}
 */
export function getServerReadAccessLevel(
    user: User | undefined,
    server: Server & { owners: { user: { id: string } }[] }
): { hasExtendedAccess: boolean; isOwner: boolean; isAdminOrSecurity: boolean } {
    if (!user) {
        return { hasExtendedAccess: false, isOwner: false, isAdminOrSecurity: false };
    }
    const isOwner = server.owners.some(owner => owner.user.id === user.id);
    const isAdminOrSecurity = user.role === 'ADMIN' || user.role === 'SECURITY';
    const hasExtendedAccess = isOwner || isAdminOrSecurity;
    return { hasExtendedAccess, isOwner, isAdminOrSecurity };
}

/**
 * Fetches subscription counts grouped by status for a given server.
 *
 * @param serverId The ID of the server.
 * @returns {Promise<Record<SubscriptionStatus, number>>} Counts for each status.
 */
export async function getSubscriptionStatusCounts(serverId: string): Promise<Record<SubscriptionStatus, number>> {
    const countsResult = await prisma.subscription.groupBy({
        by: ['status'],
        where: { serverId: serverId },
        _count: {
            status: true,
        },
    });

    // Initialize counts with 0 for all possible statuses
    const statusCounts: Record<SubscriptionStatus, number> = {
        PENDING: 0,
        ACTIVE: 0,
        BLOCKED: 0,
    };

    // Populate counts from the query result
    countsResult.forEach(item => {
        statusCounts[item.status] = item._count.status;
    });

    return statusCounts;
}