import { checkAuth } from "~/server/utils/userUtils";
import { defineEventHandler, createError, readBody, getRouterParam } from "h3"; // Added getRouterParam
import prisma from "../../utils/prisma";
import { z, ZodError } from "zod"; // Add Zod for value validation

// Define a schema for the expected body (only the value)
// Use z.any() to allow any valid JSON value (object, array, primitive)
const updateSettingSchema = z
  .object({
    value: z.any(), // Allow any type that can be JSON-serialized
  })
  .strict();

export default defineEventHandler(async (event) => {
  try {
    // Check authentication
    const currentUser = checkAuth(event);

    // Check authorization - only ADMIN users can update settings
    if (currentUser.role !== "ADMIN") {
      throw createError({
        statusCode: 403,
        statusMessage: "Forbidden: Only admins can update settings",
      });
    }

    // Get key from URL parameter
    const key = getRouterParam(event, "key");
    if (!key) {
      throw createError({
        statusCode: 400,
        statusMessage: "Setting key is required in the URL path",
      });
    }

    // Read and validate request body for the 'value'
    const body = await readBody(event);
    const validationResult = updateSettingSchema.safeParse(body);

    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: "Validation Error: Invalid request body",
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const { value } = validationResult.data;

    // Update the setting
    const updatedSetting = await prisma.settings.update({
      where: {
        key: key, // Use key from URL
      },
      data: {
        // Prisma expects JsonValue for Json fields
        // The validated 'value' should be directly usable if it's a valid JSON structure
        value: value as any, // Cast to any or Prisma.JsonValue if needed
      },
    });

    return { setting: updatedSetting };
  } catch (error: unknown) {
    // Catch specific error types
    console.error(
      `Error updating setting '${getRouterParam(event, "key")}':`,
      error
    );
    if (
      error instanceof ZodError ||
      (error instanceof Error && "statusCode" in error)
    ) {
      throw error;
    }
    // Handle Prisma error if setting key not found
    if (error instanceof Error && "code" in error && error.code === "P2025") {
      throw createError({
        statusCode: 404,
        statusMessage: `Setting with key '${getRouterParam(
          event,
          "key"
        )}' not found`,
      });
    }
    throw createError({
      statusCode: 500,
      statusMessage: "Error updating setting",
    });
  }
});
