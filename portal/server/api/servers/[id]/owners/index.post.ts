// /home/alex/go-ai/gate4ai/www/server/api/servers/[id]/owners/index.post.ts
import { defineEventHandler, getRouterParam, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
import prisma from '../../../../utils/prisma';
import { checkServerModificationRights } from '../../../../utils/serverPermissions'; // Import permission check

// Schema for request body validation
const addOwnerSchema = z.object({
  email: z.string().email("Invalid email format"),
}).strict();

export default defineEventHandler(async (event) => {
  const serverId = getRouterParam(event, 'id');
  if (!serverId) {
    throw createError({ statusCode: 400, statusMessage: 'Server ID is required' });
  }

  try {
    // 1. Check if the current user has rights to modify this server
    await checkServerModificationRights(event, serverId); // Throws 401/403/404 if not allowed

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

    // 4. Add the user as an owner (connect the relation)
    const createdLink = await prisma.serverOwner.create({
      data: {
        server: { connect: { id: serverId } },
        user: { connect: { id: userToAdd.id } },
      },
    });
    
    const updatedServer = await prisma.server.findUnique({
      where: { id: serverId },
      include: { 
        owners: {
          select: {
            user: {
              select: {
                id: true,
                name: true,
                email: true,
              },
            },
          },
        },
       },
    });


    return updatedServer?.owners;

  } catch (error: unknown) {
    console.error(`Error adding owner to server ${serverId}:`, error);
    if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) { // Re-throw Zod errors and H3 errors
      throw error;
    }
    // Handle potential Prisma errors (e.g., user already an owner - though connect handles this gracefully)
    // if (error.code === 'Pxxxx') { ... }
    throw createError({ statusCode: 500, statusMessage: 'Failed to add owner' });
  }
});