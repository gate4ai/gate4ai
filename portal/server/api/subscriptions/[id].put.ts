// /home/alex/go-ai/gate4ai/www/server/api/subscriptions/[id].put.ts
// Handles PUT /api/subscriptions/{subscriptionId} (Update Status)
import { defineEventHandler, getRouterParam, readBody, createError } from "h3";
import { z, ZodError } from "zod";
import prisma from "../../utils/prisma";
import { checkAuth } from "../../utils/userUtils";
import { getServerReadAccessLevel } from "../../utils/serverPermissions";
import type { SubscriptionStatus } from "@prisma/client"; // Import enum

const updateSubscriptionSchema = z
  .object({
    status: z.nativeEnum(["PENDING", "ACTIVE", "BLOCKED"] as const), // Validate against enum values
  })
  .strict();

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated
  const subscriptionId = getRouterParam(event, "id");

  if (!subscriptionId) {
    throw createError({
      statusCode: 400,
      statusMessage: "Subscription ID is required",
    });
  }

  try {
    // 1. Validate body
    const body = await readBody(event);
    const validationResult = updateSubscriptionSchema.safeParse(body);
    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: "Validation Error",
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const { status } = validationResult.data;

    // 2. Find subscription and related server for permission check
    const subscription = await prisma.subscription.findUnique({
      where: { id: subscriptionId },
      include: {
        server: {
          // Include server and its owners for permission check
          select: { id: true, owners: { select: { id: true } } },
        },
      },
    });

    if (!subscription || !subscription.server) {
      // Check server relation exists
      throw createError({
        statusCode: 404,
        statusMessage: "Subscription or associated server not found",
      });
    }

    // 3. Permission Check: Only Admin/Security or Server Owner can change status
    const { isOwner, isAdminOrSecurity } = getServerReadAccessLevel(
      user,
      subscription.server
    );
    if (!isAdminOrSecurity && !isOwner) {
      throw createError({
        statusCode: 403,
        statusMessage:
          "Forbidden: You do not have permission to change subscription status.",
      });
    }

    // 4. Update the subscription status
    const updatedSubscription = await prisma.subscription.update({
      where: { id: subscriptionId },
      data: { status: status as SubscriptionStatus }, // Cast status after validation
      select: {
        // Return necessary fields
        id: true,
        serverId: true,
        userId: true,
        status: true,
        // Optionally include user/server details if needed by frontend
        user: { select: { id: true, name: true, email: true } },
      },
    });

    return updatedSubscription;
  } catch (error: unknown) {
    console.error(`Error updating subscription ${subscriptionId}:`, error);
    if (
      error instanceof ZodError ||
      (error instanceof Error && "statusCode" in error)
    ) {
      throw error;
    }
    throw createError({
      statusCode: 500,
      statusMessage: "Failed to update subscription status",
    });
  }
});
