import prisma from '../../utils/prisma';
import { defineEventHandler, createError } from 'h3';

export default defineEventHandler(async (_event) => {
  console.log('[API /settings/frontend] Request received.'); // Add entry log
  try {
    const settings = await prisma.settings.findMany({
      where: {
        frontend: true // No need to cast 'as boolean'
      },
      // Optionally select only needed fields if 'value' can be large
      // select: { key: true, value: true }
    });

    console.log(`[API /settings/frontend] Found ${settings.length} frontend settings.`); // Log success

    // No need to disconnect: await prisma.$disconnect() - REMOVE THIS LINE

    return settings; // Return directly
  } catch (error) {
    console.error('Error fetching frontend settings:', error);
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to fetch frontend settings'
    });
  }
});