import { defineEventHandler, getRouterParam, createError } from 'h3';
import { checkSubscriptionAccessRights } from '../../../utils/serverPermissions';
import type { Prisma } from '@prisma/client';

export default defineEventHandler(async (event) => {
  const subscriptionId = getRouterParam(event, 'id');
  if (!subscriptionId) {
    throw createError({ statusCode: 400, statusMessage: 'Subscription ID is required' });
  }

  try {
    // Check permissions (subscriber, owner, admin)
    const { subscription } = await checkSubscriptionAccessRights(event, subscriptionId);

    // Prisma stores JSONB as Prisma.JsonValue
    const headerValues = subscription.headerValues as Prisma.JsonObject | null;

    // Return the values or an empty object if null
    return headerValues ?? {};

  } catch (error: unknown) {
    console.error(`Error fetching subscription headers for ID ${subscriptionId}:`, error);
    if (error instanceof Error && 'statusCode' in error) {
      throw error; // Re-throw H3 errors (like 403, 404)
    }
    throw createError({ statusCode: 500, statusMessage: 'Failed to fetch subscription headers' });
  }
});