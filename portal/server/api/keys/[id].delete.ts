//////////////////////////
// /home/alex/go-ai/gate4ai/www/server/api/keys/[id].delete.ts
//////////////////////////
import { PrismaClient } from '@prisma/client';
import { defineEventHandler, createError, getRouterParam } from 'h3';
import { checkAuth } from '~/server/utils/userUtils';

const prisma = new PrismaClient();

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated
  const keyId = getRouterParam(event, 'id');

  if (!keyId) {
    throw createError({ statusCode: 400, statusMessage: 'API key ID is required' });
  }

  try {
    // Find the key first to check ownership
    const apiKey = await prisma.apiKey.findUnique({
      where: {
        id: keyId,
      },
      select: {
        userId: true, // Select only userId for the check
      },
    });

    if (!apiKey) {
      // Even if not found, return 204 for idempotency, or 404 if strictness desired
      // Let's return 404 for clarity
      throw createError({ statusCode: 404, statusMessage: 'API key not found' });
    }

    // Permission Check: Ensure the user owns the key or is admin/security
    const isAdminOrSecurity = user.role === 'ADMIN' || user.role === 'SECURITY';
    if (apiKey.userId !== user.id && !isAdminOrSecurity) {
      throw createError({
        statusCode: 403,
        statusMessage: 'Forbidden: You do not have permission to delete this API key.',
      });
    }

    // Delete the API key
    await prisma.apiKey.delete({
      where: {
        id: keyId,
      },
    });

    event.node.res.statusCode = 204; // No Content on successful deletion
    return; // Explicitly return nothing

  } catch (error: unknown) {
    console.error(`Error deleting API key ${keyId}:`, error);

    if (error instanceof Error && 'statusCode' in error) {
      throw error; // Re-throw H3 errors (like 403, 404)
    }

    // Handle potential Prisma errors during delete if necessary
    // (e.g., if deletion fails for some reason)

    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to delete API key',
    });
  } finally {
     await prisma.$disconnect();
  }
});