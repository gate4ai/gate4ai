import { defineEventHandler, readBody, createError } from "h3";
import { z, ZodError } from "zod";
import prisma from "../../utils/prisma";
import { checkAuth } from "../../utils/userUtils";
import { getServerReadAccessLevel } from "../../utils/serverPermissions"; // Import read access helper
import { Prisma } from "@prisma/client"; // Import Prisma namespace

// Schema for the required serverId
const subscribeBaseSchema = z.object({
  serverId: z.string().uuid("Invalid Server ID format"),
});

// Schema for the optional headerValues
const headerValuesSchema = z
  .record(z.string(), z.string(), {
    invalid_type_error: "Header values must be strings.",
  })
  .optional()
  .nullable(); // Optional map[string]string

// Combine schemas (allow extra fields for now, strict() might be too strict if client sends more)
const subscribeSchema = subscribeBaseSchema.extend({
  headerValues: headerValuesSchema,
}); //.strict(); // Consider if strict is needed

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated

  try {
    // 1. Validate body
    const body = await readBody(event);
    const validationResult = subscribeSchema.safeParse(body);
    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: "Validation Error",
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const { serverId, headerValues } = validationResult.data;

    // 2. Fetch server to check ownership/admin status AND its template
    const server = await prisma.server.findUnique({
      where: { id: serverId },
      include: {
        owners: { select: { user: { select: { id: true } } } },
        subscriptionHeaderTemplate: true, // Fetch the template
      },
    });
    if (!server) {
      throw createError({ statusCode: 404, statusMessage: "Server not found" });
    }

    // 3. Permission Check: Owners, Admins, Security cannot subscribe
    const { isOwner, isAdminOrSecurity } = getServerReadAccessLevel(
      user,
      server
    );
    if (isOwner || isAdminOrSecurity) {
      throw createError({
        statusCode: 403,
        statusMessage:
          "Owners, Admins, and Security personnel cannot subscribe.",
      });
    }

    // 4. Validate provided headerValues against the template
    const validatedHeaderValues: Record<string, string> = {};
    const validationErrors: Record<string, string[]> = {};
    const template = server.subscriptionHeaderTemplate || [];
    const templateKeys = new Set(template.map((item) => item.key));

    // Check required fields are present in the input
    for (const item of template) {
      const providedValue = headerValues?.[item.key];
      if (item.required && (!providedValue || providedValue.trim() === "")) {
        validationErrors[item.key] = [`Header '${item.key}' is required.`];
      } else if (providedValue) {
        // Only store keys that are actually in the template
        validatedHeaderValues[item.key] = providedValue;
      }
    }

    // Check if extra headers were provided (and reject/ignore)
    for (const providedKey in headerValues) {
      if (!templateKeys.has(providedKey)) {
        validationErrors[providedKey] = [
          `Header '${providedKey}' is not defined for this server.`,
        ];
      }
    }

    if (Object.keys(validationErrors).length > 0) {
      throw createError({
        statusCode: 400,
        statusMessage:
          "Validation Error: Provided headers do not meet requirements.",
        data: validationErrors,
      });
    }
    // --- End Header Validation ---

    // 5. Create subscription
    const newSubscription = await prisma.subscription.create({
      data: {
        userId: user.id,
        serverId: serverId,
        status: "ACTIVE", // Defaulting to ACTIVE, adjust if PENDING needed
        headerValues:
          Object.keys(validatedHeaderValues).length > 0
            ? (validatedHeaderValues as Prisma.JsonObject)
            : Prisma.JsonNull, // Save validated headers or null
      },
      select: {
        // Return necessary fields
        id: true,
        serverId: true,
        userId: true,
        status: true,
        headerValues: true, // Return the saved headers
      },
    });

    event.node.res.statusCode = 201; // Created
    return newSubscription;
  } catch (error: unknown) {
    console.error("Error creating subscription:", error);
    if (
      error instanceof ZodError ||
      (error instanceof Error && "statusCode" in error)
    ) {
      throw error;
    }
    // Handle Prisma unique constraint error (already subscribed)
    if (error instanceof Error && "code" in error && error.code === "P2002") {
      throw createError({
        statusCode: 409,
        statusMessage: "Already subscribed to this server.",
      });
    }
    throw createError({
      statusCode: 500,
      statusMessage: "Failed to subscribe",
    });
  }
});
