import { defineEventHandler, getRouterParam, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
import prisma from '../../utils/prisma';
import { checkServerModificationRights } from '../../utils/serverPermissions'; // Adjust path if needed
// Import enums for validation - adjust path as needed
import { ServerStatus, ServerAvailability, ServerType } from '@prisma/client';

// Define the schema for updating a server.
// Make fields optional as the client might only send updated ones.
// Slug update might need special handling or be disallowed.
const updateServerSchema = z.object({
  name: z.string().min(1, 'Name is required').max(100, 'Name must be 100 characters or less').optional(),
  // slug: z.string().min(1).regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/, 'Invalid slug format').optional(), // Decide if slug updates are allowed
  type: z.nativeEnum(ServerType).optional(),
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

    // Prevent changing the slug via this endpoint if desired.
    // If slug changes are allowed, ensure the new slug is unique before updating.
    // if (validatedData.slug && validatedData.slug !== serverSlug) {
    //   // Add logic here to check if the new slug is available
    //   // const existing = await prisma.server.findUnique({ where: { slug: validatedData.slug }});
    //   // if (existing) throw createError(...);
    // }

    // 3. Update the server data using the fetched server ID
    const updatedServer = await prisma.server.update({
      where: { id: server.id }, // Update using the unique internal ID
      data: {
        ...validatedData, // Spread validated optional fields
        // Exclude slug explicitly if not allowing updates:
        // slug: undefined,
      },
       // Select the fields to return in the response
       select: {
        id: true,
        slug: true,
        name: true,
        description: true,
        website: true,
        email: true,
        imageUrl: true,
        type: true,
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
     if (error instanceof Error && 'code' in error && error.code === 'P2002') { // Check specific target if needed
         throw createError({ statusCode: 409, statusMessage: 'Update failed due to conflicting data (e.g., slug already exists).' });
     }

    // Handle case where server wasn't found by checkServerModificationRights (should throw 404)
    // but catch Prisma's P2025 just in case something slips through
     if (error instanceof Error && 'code' in error && error.code === 'P2025') {
        throw createError({ statusCode: 404, statusMessage: `Server with slug '${serverSlug}' not found for update.` });
     }

    // Generic fallback error
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to update server due to an unexpected error.',
    });
  }
});