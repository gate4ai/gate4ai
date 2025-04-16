import { defineEventHandler, getRouterParam, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
import prisma from '../../utils/prisma';
import { checkServerModificationRights } from '../../utils/serverPermissions'; // Adjust path if needed
// Import enums for validation - adjust path as needed
import { ServerStatus, ServerAvailability, ServerProtocol } from '@prisma/client';

// Define the schema for updating a server.
// Make fields optional as the client might only send updated ones.
const updateServerSchema = z.object({
  name: z.string().min(1, 'Name is required').max(100, 'Name must be 100 characters or less').optional(),
  // slug is not allowed to be updated in this endpoint
  protocol: z.nativeEnum(ServerProtocol).optional(),
  protocolVersion: z.string().optional().nullable(),
  description: z.string().max(500, "Description too long").optional().nullable(),
  website: z.string().url('Invalid URL format').optional().nullable(),
  email: z.string().email('Invalid email format').optional().nullable(),
  imageUrl: z.string().url('Invalid URL format').optional().nullable(),
  serverUrl: z.string().url('Server URL must be a valid URL').optional(),
  status: z.nativeEnum(ServerStatus).optional(),
  availability: z.nativeEnum(ServerAvailability).optional(),
  // tools are usually managed via separate endpoints, not included in main server update
}).strict(); // Use strict to prevent unexpected fields

export default defineEventHandler(async (event) => {
  const serverSlug = getRouterParam(event, 'slug'); // Get slug from URL

  if (!serverSlug) {
    throw createError({ statusCode: 400, statusMessage: 'Server slug is required' });
  }

  try {
    // 1. Check permissions using the slug. This also fetches the server ID.
    const { server } = await checkServerModificationRights(event, serverSlug);

    // 2. Read and validate the request body
    const body = await readBody(event);
    const validationResult = updateServerSchema.safeParse(body);

    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: 'Validation Error',
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const validatedData = validationResult.data;

    // 3. Prepare the update data with proper Prisma types
    const updateData: any = {};
    
    // Add fields conditionally
    if (validatedData.name !== undefined) updateData.name = validatedData.name;
    if (validatedData.protocol !== undefined) updateData.protocol = validatedData.protocol;
    if (validatedData.serverUrl !== undefined) updateData.serverUrl = validatedData.serverUrl;
    if (validatedData.status !== undefined) updateData.status = validatedData.status;
    if (validatedData.availability !== undefined) updateData.availability = validatedData.availability;
    
    // Handle nullable fields
    if (validatedData.protocolVersion !== undefined) updateData.protocolVersion = validatedData.protocolVersion;
    if (validatedData.description !== undefined) updateData.description = validatedData.description;
    if (validatedData.website !== undefined) updateData.website = validatedData.website;
    if (validatedData.email !== undefined) updateData.email = validatedData.email;
    if (validatedData.imageUrl !== undefined) updateData.imageUrl = validatedData.imageUrl;

    // 4. Update the server data using the fetched server ID
    const updatedServer = await prisma.server.update({
      where: { id: server.id }, // Update using the unique internal ID
      data: updateData,
      // Select the fields to return in the response
      select: {
        id: true,
        slug: true,
        name: true,
        description: true,
        website: true,
        email: true,
        imageUrl: true,
        protocol: true, // Ensure protocol is included
        protocolVersion: true,
        serverUrl: true,
        status: true,
        availability: true,
        createdAt: true,
        updatedAt: true,
        tools: { include: { parameters: true } }, // Include related data if needed by frontend
        owners: { select: { user: { select: { id: true, name: true, email: true } } } },
      }
    });

    return updatedServer;

  } catch (error: unknown) {
    console.error(`Error updating server with slug ${serverSlug}:`, error);

    if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) {
      // Re-throw validation and H3 errors (like permission errors)
      throw error;
    }

     // Handle potential Prisma errors (e.g., unique constraint violation if slug is updated and conflicts)
     if (error instanceof Error && 'code' in error && (error as any).code === 'P2002') { // Check specific target if needed
         throw createError({ statusCode: 409, statusMessage: 'Update failed due to conflicting data (e.g., slug already exists).' });
     }

    // Handle case where server wasn't found by checkServerModificationRights (should throw 404)
    // but catch Prisma's P2025 just in case something slips through
     if (error instanceof Error && 'code' in error && (error as any).code === 'P2025') {
        throw createError({ statusCode: 404, statusMessage: `Server with slug '${serverSlug}' not found for update.` });
     }

    // Generic fallback error
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to update server due to an unexpected error.',
    });
  }
});