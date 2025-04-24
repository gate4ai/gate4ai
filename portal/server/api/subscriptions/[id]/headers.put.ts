import { defineEventHandler, getRouterParam, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
import { checkSubscriptionAccessRights } from '../../../utils/serverPermissions';
import prisma from '../../../utils/prisma';
import type { Prisma } from '@prisma/client';

// Correct schema: value type first, then refinement
const headerValuesSchema = z.record(z.string()) // Define record with string values
  .refine( // Add refinement separately
    (val) => Object.values(val).every((v) => typeof v === 'string'),
    { message: "Header values must be strings" }
  );


export default defineEventHandler(async (event) => {
  // ... (rest of the handler remains the same)
  const subscriptionId = getRouterParam(event, 'id');
  if (!subscriptionId) { throw createError({ statusCode: 400, statusMessage: 'Subscription ID is required' }); }
  try {
    const { subscription, server, isSubscriber } = await checkSubscriptionAccessRights(event, subscriptionId);
    if (!isSubscriber) { throw createError({ statusCode: 403, statusMessage: 'Forbidden: Only the subscriber can update these headers.' }); }
    const body = await readBody(event);
    const validationResult = headerValuesSchema.safeParse(body);
    if (!validationResult.success) { throw createError({ statusCode: 400, statusMessage: 'Validation Error: Invalid header values format.', data: validationResult.error.flatten().fieldErrors, }); }
    const newHeaderValues = validationResult.data;
    const template = server.subscriptionHeaderTemplate; const validationErrors: Record<string, string[]> = {};
    for (const item of template) { if (item.required && (!newHeaderValues[item.key] || newHeaderValues[item.key].trim() === '')) { validationErrors[item.key] = [`Header '${item.key}' is required.`]; } }
    const templateKeys = new Set(template.map(item => item.key));
    for (const providedKey in newHeaderValues) { if (!templateKeys.has(providedKey)) { delete newHeaderValues[providedKey]; } }
    if (Object.keys(validationErrors).length > 0) { throw createError({ statusCode: 400, statusMessage: 'Validation Error: Header values do not match template requirements.', data: validationErrors, }); }
    const updatedSubscription = await prisma.subscription.update({ where: { id: subscriptionId }, data: { headerValues: newHeaderValues as Prisma.JsonObject, }, select: { headerValues: true }, });
    return updatedSubscription.headerValues ?? {};
  } catch (error: unknown) { console.error(`Error updating subscription headers for ID ${subscriptionId}:`, error); if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) { throw error; } throw createError({ statusCode: 500, statusMessage: 'Failed to update subscription headers' }); }
});