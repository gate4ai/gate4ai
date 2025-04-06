import { defineEventHandler, getRouterParam, createError } from 'h3';
// Import the specific check function
import { checkServerModificationRights } from '../../utils/serverPermissions';
import prisma from '../../utils/prisma';

export default defineEventHandler(async (event) => {
  const id = getRouterParam(event, 'id');

  if (!id) {
    throw createError({ statusCode: 400, statusMessage: 'Server ID is required' });
  }

  try {
    // 1. Check permissions BEFORE attempting deletion
    // This function throws an error if the user is not authorized.
    // It also fetches the server, but we don't need to reuse it here.
    await checkServerModificationRights(event, id);

    // 2. If permission check passes, proceed with deletion
    await prisma.server.delete({
      where: { id },
    });

    // Set response code for successful deletion with no content
    event.node.res.statusCode = 204;
    return; // Explicitly return nothing

  } catch (error: unknown) {
    // Log the specific error
    console.error(`Error deleting server ${id}:`, error);

    // If the error already has a statusCode (e.g., from checkServerModificationRights or Prisma known errors), re-throw it
    if (error instanceof Error && 'statusCode' in error) {
      throw error;
    }

    // Otherwise, throw a generic server error
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to delete server due to an unexpected error.',
    });
  }
});