// /home/alex/go-ai/gate4ai/www/server/api/servers/[id].put.ts
import { defineEventHandler, getRouterParam, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
// Import the specific check function
import { checkServerModificationRights } from '../../utils/serverPermissions';
import prisma from '../../utils/prisma';
import  { ServerStatus, ServerAvailability } from '@prisma/client'; // Import enums if needed for schema

// Stricter validation: ensures no extra fields are passed
// Only fields modifiable by owners/admins should be here.
// Owners/Admins might also want to change status or availability.
const updateServerSchema = z.object({
  name: z.string().min(1, "Name cannot be empty").max(100).optional(),
  description: z.string().max(500, "Description too long").optional().nullable(),
  website: z.string().url("Invalid URL format").optional().nullable(),
  email: z.string().email("Invalid email format").optional().nullable(),
  imageUrl: z.string().url("Invalid URL format").optional().nullable(),
  serverUrl: z.string().url("Invalid URL format").optional(), // Only for extended access
  status: z.nativeEnum(ServerStatus).optional(), // Allow updating status
  availability: z.nativeEnum(ServerAvailability).optional(), // Allow updating availability
  // ownerIds: z.array(z.string().uuid()).optional() // Example: If you want to allow changing owners
}).strict(); // Use strict to prevent unexpected fields

export default defineEventHandler(async (event) => {
  const id = getRouterParam(event, 'id');

  if (!id) {
    throw createError({ statusCode: 400, statusMessage: 'Server ID is required' });
  }

  try {
    // 1. Check permissions first. This throws if not authorized.
    // We get user and server data back, but don't strictly need them here yet.
    await checkServerModificationRights(event, id);

    // 2. Read and validate the request body
    const body = await readBody(event);
    const validatedData = updateServerSchema.parse(body);

    // Potential enhancement: If allowing owner changes, add logic here
    // const ownerUpdateData = validatedData.ownerIds
    //   ? { owners: { set: validatedData.ownerIds.map(id => ({ id })) } }
    //   : {};
    // delete validatedData.ownerIds; // Remove from direct update data

    // 3. Update the server in the database
    const updatedServer = await prisma.server.update({
      where: { id },
      // data: { ...validatedData, ...ownerUpdateData },
      data: validatedData, // Pass only validated fields from schema
      select: { // Select fields for the response (consistent with GET)
        id: true,
        name: true,
        description: true,
        website: true,
        email: true,
        imageUrl: true,
        serverUrl: true, // Return extended info
        status: true,
        availability: true,
        createdAt: true,
        updatedAt: true,
        owners: {
            select: { user: true }
        }
      },
    });

    return updatedServer;

  } catch (error: unknown) {
    console.error(`Error updating server ${id}:`, error);

    if (error instanceof ZodError) {
      // Format Zod errors for a cleaner response
      throw createError({
        statusCode: 400,
        statusMessage: 'Validation failed',
        // Use flatten() for a simpler error structure
        data: error.flatten().fieldErrors,
      });
    }

    // Re-throw errors with status codes (like 403 Forbidden from permission check)
    if (error instanceof Error && 'statusCode' in error) {
      throw error;
    }

    // Handle other potential Prisma errors (e.g., unique constraint) if necessary
    // if (error instanceof Prisma.PrismaClientKnownRequestError) { ... }

    // Generic fallback error
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to update server due to an unexpected error.',
    });
  }
});