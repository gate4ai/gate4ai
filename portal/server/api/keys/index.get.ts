//////////////////////////
// /home/alex/go-ai/gate4ai/www/server/api/keys/index.get.ts
//////////////////////////
import { PrismaClient } from '@prisma/client';
import { defineEventHandler, createError } from 'h3';
import { checkAuth } from '~/server/utils/userUtils';

const prisma = new PrismaClient();

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated

  try {
    const apiKeys = await prisma.apiKey.findMany({
      where: {
        userId: user.id,
      },
      select: {
        id: true,
        name: true,
        keyHash: true, // Now selecting keyHash instead of key
        createdAt: true,
        lastUsed: true,
      },
      orderBy: {
        createdAt: 'desc',
      },
    });

    // Only return minimal information - no need to mask since we only have hashes now
    const keyList = apiKeys.map(apiKey => {
        return {
            id: apiKey.id,
            name: apiKey.name,
            // Optionally take first and last chars of hash for display
            keyHash: apiKey.keyHash.substring(0, 8) + '...',
            createdAt: apiKey.createdAt,
            lastUsed: apiKey.lastUsed,
        };
    });

    return keyList;

  } catch (error: unknown) {
    console.error('Error fetching API keys:', error);

    if (error instanceof Error && 'statusCode' in error) {
      throw error; // Re-throw H3 errors
    }

    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to fetch API keys',
    });
  } finally {
      await prisma.$disconnect();
  }
});