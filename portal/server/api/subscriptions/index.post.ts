// /home/alex/go-ai/gate4ai/www/server/api/subscriptions/index.post.ts
// Handles POST /api/subscriptions (Subscribe)
import { defineEventHandler, readBody, createError } from 'h3';
import { z, ZodError } from 'zod';
import prisma from '../../utils/prisma';
import { checkAuth } from '../../utils/userUtils';
import { getServerReadAccessLevel } from '../../utils/serverPermissions'; // Import read access helper

const subscribeSchema = z.object({
  serverId: z.string().uuid("Invalid Server ID format"),
}).strict();

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated

  try {
    // 1. Validate body
    const body = await readBody(event);
    const validationResult = subscribeSchema.safeParse(body);
    if (!validationResult.success) {
      throw createError({ statusCode: 400, statusMessage: 'Validation Error', data: validationResult.error.flatten().fieldErrors });
    }
    const { serverId } = validationResult.data;

    // 2. Fetch server to check ownership/admin status
    const server = await prisma.server.findUnique({
      where: { id: serverId },
      include: { owners: { select: { user: { select: { id: true } } } } },
    });
    if (!server) {
      throw createError({ statusCode: 404, statusMessage: 'Server not found' });
    }

    // 3. Permission Check: Owners, Admins, Security cannot subscribe
    const { isOwner, isAdminOrSecurity } = getServerReadAccessLevel(user, server);
    if (isOwner || isAdminOrSecurity) {
      throw createError({ statusCode: 403, statusMessage: 'Owners, Admins, and Security personnel cannot subscribe.' });
    }

    // 4. Create subscription (Prisma handles unique constraint gracefully with create)
    // Determine initial status based on server availability? (e.g., PENDING for SUBSCRIPTION, ACTIVE for PUBLIC?)
    // Let's default to ACTIVE for simplicity for now, assuming PUBLIC/SUBSCRIPTION allow immediate active state.
    // Adjust status logic here if needed based on server.availability
    const newSubscription = await prisma.subscription.create({
      data: {
        userId: user.id,
        serverId: serverId,
        status: 'ACTIVE', // Defaulting to ACTIVE, adjust if PENDING needed
      },
      select: { // Return necessary fields
        id: true,
        serverId: true,
        userId: true,
        status: true,
      }
    });

    event.node.res.statusCode = 201; // Created
    return newSubscription;

  } catch (error: unknown) {
    console.error('Error creating subscription:', error);
    if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) {
      throw error;
    }
    // Handle Prisma unique constraint error (already subscribed)
    if (error instanceof Error && 'code' in error && error.code === 'P2002') {
       throw createError({ statusCode: 409, statusMessage: 'Already subscribed to this server.' });
    }
    throw createError({ statusCode: 500, statusMessage: 'Failed to subscribe' });
  }
});