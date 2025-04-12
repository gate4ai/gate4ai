import { defineEventHandler, getRouterParam, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
import prisma from '../../../../utils/prisma';
import { checkServerModificationRights } from '../../../../utils/serverPermissions'; // Adjust path

// Schema for request body validation
const addOwnerSchema = z.object({
  email: z.string().email("Invalid email format"),
}).strict();

export default defineEventHandler(async (event) => {
  const serverSlug = getRouterParam(event, 'slug'); // Use slug
  if (!serverSlug) {
    throw createError({ statusCode: 400, statusMessage: 'Server slug is required' });
  }

  try {
    // 1. Check if the current user has rights to modify this server using the slug
    // This also fetches the server ID.
    const { server } = await checkServerModificationRights(event, serverSlug);

    // 2. Validate request body
    const body = await readBody(event);
    const validationResult = addOwnerSchema.safeParse(body);
    if (!validationResult.success) {
      throw createError({ statusCode: 400, statusMessage: 'Validation Error', data: validationResult.error.flatten().fieldErrors });
    }
    const { email } = validationResult.data;

    // 3. Find the user to be added by email
    const userToAdd = await prisma.user.findUnique({
      where: { email },
      select: { id: true }, // Only need the ID
    });

    if (!userToAdd) {
      throw createError({ statusCode: 404, statusMessage: `User with email ${email} not found` });
    }

    // 4. Add the user as an owner using the actual server ID (UUID)
    await prisma.serverOwner.create({
      data: {
        serverId: server.id, // Use the internal ID from fetched server
        userId: userToAdd.id,
      },
    });

    // 5. Fetch the updated owner list using the server ID
    const updatedOwners = await prisma.serverOwner.findMany({
       where: { serverId: server.id }, // Use internal ID
       select: {
         user: {
           select: {
             id: true,
             name: true,
             email: true,
           },
         },
       },
     });

    // Return the updated list of owners (just the user part)
    return updatedOwners.map(o => o.user);

  } catch (error: unknown) {
    console.error(`Error adding owner to server with slug ${serverSlug}:`, error);
    if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) { // Re-throw Zod and H3 errors
      throw error;
    }
    // Handle potential Prisma errors (e.g., user already an owner - P2002)
     if (error instanceof Error && 'code' in error && error.code === 'P2002') {
        throw createError({ statusCode: 409, statusMessage: 'User is already an owner of this server.' });
     }
    throw createError({ statusCode: 500, statusMessage: 'Failed to add owner' });
  }
});