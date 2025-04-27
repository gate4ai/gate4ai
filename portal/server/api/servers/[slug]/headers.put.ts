import { defineEventHandler, getRouterParam, readBody, createError } from "h3";
import { z, ZodError } from "zod";
import { checkServerModificationRights } from "../../../utils/serverPermissions";
import prisma from "../../../utils/prisma";
import type { Prisma } from "@prisma/client";

// Correct schema: value type first, then refinement
const headersSchema = z
  .record(z.string()) // Define record with string values
  .refine(
    // Add refinement separately
    (val) => Object.values(val).every((v) => typeof v === "string"),
    { message: "Header values must be strings" }
  );

export default defineEventHandler(async (event) => {
  // ... (rest of the handler remains the same)
  const slug = getRouterParam(event, "slug");
  if (!slug) {
    throw createError({
      statusCode: 400,
      statusMessage: "Server slug is required",
    });
  }
  try {
    const { server } = await checkServerModificationRights(event, slug);
    const body = await readBody(event);
    const validationResult = headersSchema.safeParse(body);
    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: "Validation Error: Invalid headers format.",
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const newHeaders = validationResult.data;
    const updatedServer = await prisma.server.update({
      where: { id: server.id },
      data: { headers: newHeaders as Prisma.JsonObject },
      select: { headers: true },
    });
    return updatedServer.headers ?? {};
  } catch (error: unknown) {
    console.error(`Error updating server headers for slug ${slug}:`, error);
    if (
      error instanceof ZodError ||
      (error instanceof Error && "statusCode" in error)
    ) {
      throw error;
    }
    throw createError({
      statusCode: 500,
      statusMessage: "Failed to update server headers",
    });
  }
});
