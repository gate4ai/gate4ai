import { defineEventHandler, getRouterParam, createError } from 'h3';
import prisma from '../../../utils/prisma';

export default defineEventHandler(async (event) => {
  const slug = getRouterParam(event, 'slug');

  if (!slug) {
    throw createError({ statusCode: 400, statusMessage: 'Slug parameter is required' });
  }

  // Basic slug format check (optional, but good practice)
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(slug)) {
      throw createError({ statusCode: 400, statusMessage: 'Invalid slug format.' });
  }

  try {
    const count = await prisma.server.count({
      where: { slug: slug },
    });

    return { exists: count > 0 };
  } catch (error) {
    console.error(`Error checking slug uniqueness for "${slug}":`, error);
    // Don't throw 500, let the client decide how to handle check failure maybe?
    // Or return an error indicator? For now, let's return exists: false and log.
    // A more robust approach might return an error object.
    // throw createError({ statusCode: 500, statusMessage: 'Failed to check slug uniqueness' });
     return { exists: false, error: 'Failed to check uniqueness' }; // Indicate check failed but don't block UI unnecessarily
  }
});