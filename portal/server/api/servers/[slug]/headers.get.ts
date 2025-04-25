import { defineEventHandler, getRouterParam, createError } from 'h3';
import { checkServerModificationRights } from '../../../utils/serverPermissions';
import prisma from '../../../utils/prisma';
import type { Prisma } from '@prisma/client';

export default defineEventHandler(async (event) => {
  const slug = getRouterParam(event, 'slug');
  if (!slug) {
    throw createError({ statusCode: 400, statusMessage: 'Server slug is required' });
  }

  try {
    // Check permissions (only owners/admins can view raw server headers)
    const { server } = await checkServerModificationRights(event, slug);

    // Prisma stores JSONB as Prisma.JsonValue
    const headers = server.headers as Prisma.JsonObject | null;

    // Return the headers or an empty object if null
    return headers ?? {};

  } catch (error: unknown) {
    console.error(`Error fetching server headers for slug ${slug}:`, error);
    if (error instanceof Error && 'statusCode' in error) {
      throw error; // Re-throw H3 errors (like 403, 404)
    }
    throw createError({ statusCode: 500, statusMessage: 'Failed to fetch server headers' });
  }
});