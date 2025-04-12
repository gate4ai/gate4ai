import { defineEventHandler, getRouterParam, createError } from 'h3';
// Import the specific check function
import { checkServerModificationRights } from '../../utils/serverPermissions'; // Path might need adjustment
import prisma from '../../utils/prisma';

export default defineEventHandler(async (event) => {
  const slug = getRouterParam(event, 'slug'); // Get slug instead of id

  if (!slug) {
    throw createError({ statusCode: 400, statusMessage: 'Server slug is required' });
  }

  try {
    // 1. Check permissions BEFORE attempting deletion using the slug
    // This function now needs to accept slug and find the server by slug.
    await checkServerModificationRights(event, slug);

    // 2. If permission check passes, proceed with deletion using the slug
    await prisma.server.delete({
      where: { slug }, // Delete by slug
    });

    // Set response code for successful deletion with no content
    event.node.res.statusCode = 204;
    return; // Explicitly return nothing

  } catch (error: unknown) {
    // Log the specific error
    console.error(`Error deleting server with slug ${slug}:`, error);

    // If the error already has a statusCode (e.g., from checkServerModificationRights or Prisma known errors), re-throw it
    if (error instanceof Error && 'statusCode' in error) {
      throw error;
    }
     // Handle Prisma error if record not found (P2025)
     if (error instanceof Error && 'code' in error && error.code === 'P2025') {
        throw createError({ statusCode: 404, statusMessage: `Server with slug '${slug}' not found.` });
     }

    // Otherwise, throw a generic server error
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to delete server due to an unexpected error.',
    });
  }
});