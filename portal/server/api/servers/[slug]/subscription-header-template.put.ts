import { defineEventHandler, getRouterParam, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
import { checkServerModificationRights } from '../../../utils/serverPermissions';
import prisma from '../../../utils/prisma';

// Schema for a single template item
const templateItemSchema = z.object({
  key: z.string().min(1, "Header key cannot be empty").regex(/^[A-Za-z0-9-]+$/, "Invalid header key format"),
  description: z.string().optional().nullable(),
  required: z.boolean().optional().default(false),
}).strict();

// Schema for the entire template array
const templateSchema = z.array(templateItemSchema);

export default defineEventHandler(async (event) => {
  const slug = getRouterParam(event, 'slug');
  if (!slug) {
    throw createError({ statusCode: 400, statusMessage: 'Server slug is required' });
  }

  try {
    // Check permissions (only owners/admins can update template)
    const { server } = await checkServerModificationRights(event, slug);

    // Read and validate the request body (expecting an array)
    const body = await readBody(event);
    const validationResult = templateSchema.safeParse(body);

    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: 'Validation Error: Invalid template format.',
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const newTemplateData = validationResult.data;

    // --- Transaction to update the template ---
    // 1. Delete existing template items for this server.
    // 2. Create new template items from the validated data.
    const updatedTemplate = await prisma.$transaction(async (tx) => {
      await tx.subscriptionHeaderTemplate.deleteMany({
        where: { serverId: server.id },
      });

      if (newTemplateData.length > 0) {
        await tx.subscriptionHeaderTemplate.createMany({
          data: newTemplateData.map(item => ({
            serverId: server.id,
            key: item.key,
            description: item.description,
            required: item.required,
          })),
        });
      }

      // Fetch and return the newly created template for confirmation
      return tx.subscriptionHeaderTemplate.findMany({
        where: { serverId: server.id },
         select: {
            id: true,
            key: true,
            description: true,
            required: true,
         },
         orderBy: { key: 'asc' },
      });
    });

    return updatedTemplate ?? []; // Return updated template or empty array

  } catch (error: unknown) {
    console.error(`Error updating subscription header template for slug ${slug}:`, error);
    if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) {
      throw error; // Re-throw validation and H3 errors
    }
    throw createError({ statusCode: 500, statusMessage: 'Failed to update subscription header template' });
  }
});