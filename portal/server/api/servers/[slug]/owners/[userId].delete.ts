// /home/alex/go-ai/gate4ai/www/server/api/servers/[id]/owners/[userId].delete.ts
import { defineEventHandler, getRouterParam, createError } from 'h3';
import prisma from '../../../../utils/prisma';
import { checkServerModificationRights } from '../../../../utils/serverPermissions'; // Import permission check

export default defineEventHandler(async (event) => {
  const serverId = getRouterParam(event, 'id');
  const userIdToRemove = getRouterParam(event, 'userId');

  if (!serverId) {
    throw createError({ statusCode: 400, statusMessage: 'Server ID is required' });
  }
  if (!userIdToRemove) {
    throw createError({ statusCode: 400, statusMessage: 'User ID to remove is required' });
  }

  try {
    // 1. Check if the current user has rights to modify this server
    const { server } = await checkServerModificationRights(event, serverId); // Get server data too

    // 2. Prevent removing the last owner
    if (server.owners.length <= 1) {
        throw createError({ statusCode: 400, statusMessage: 'Cannot remove the last owner of the server.' });
    }

    // 3. Remove the user as an owner (disconnect the relation)
    await prisma.serverOwner.delete({
      where: {
        serverId_userId: {
          serverId: serverId,
          userId: userIdToRemove,
        },
      },
    });

    const updatedServer = await prisma.server.findUnique({
      where: { id: serverId },
      include: { 
        owners: {
          select: {
            user: {
              select: {
                id: true,
                name: true,
                email: true,
              },
            },
          },
        },
       },
    });

     // Return the updated list of owners
     return updatedServer?.owners;

  } catch (error: unknown) {
    console.error(`Error removing owner ${userIdToRemove} from server ${serverId}:`, error);
    if (error instanceof Error && 'statusCode' in error) { // Re-throw H3 errors
      throw error;
    }
    // Handle potential Prisma errors (e.g., user not an owner)
    throw createError({ statusCode: 500, statusMessage: 'Failed to remove owner' });
  }
});