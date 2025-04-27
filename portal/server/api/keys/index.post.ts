//////////////////////////
// /home/alex/go-ai/gate4ai/www/server/api/keys/index.post.ts
//////////////////////////
import { PrismaClient } from "@prisma/client";
import { defineEventHandler, createError, readBody } from "h3";
import { checkAuth } from "~/server/utils/userUtils"; // Assuming you have checkAuth
import { z, ZodError } from "zod";

const prisma = new PrismaClient();

// Input validation schema
const createKeySchema = z
  .object({
    name: z
      .string()
      .min(1, "Key name cannot be empty")
      .max(100, "Key name too long"),
    keyHash: z.string().min(1, "Key hash cannot be empty"),
  })
  .strict(); // Use strict to prevent extra fields

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated

  try {
    // Validate the request body
    const body = await readBody(event);
    const validationResult = createKeySchema.safeParse(body);

    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: "Validation Error",
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const { name, keyHash } = validationResult.data;

    // Create the API key in the database
    const apiKey = await prisma.apiKey.create({
      data: {
        name: name,
        keyHash: keyHash, // Store the hash only
        userId: user.id,
      },
      // Select fields to return
      select: {
        id: true,
        name: true,
        createdAt: true,
        lastUsed: true,
      },
    });

    event.node.res.statusCode = 201; // Created
    return apiKey;
  } catch (error: unknown) {
    console.error("Error creating API key:", error);

    if (
      error instanceof ZodError ||
      (error instanceof Error && "statusCode" in error)
    ) {
      throw error; // Re-throw validation and H3 errors
    }

    throw createError({
      statusCode: 500,
      statusMessage: "Failed to create API key",
    });
  } finally {
    await prisma.$disconnect(); // Disconnect prisma client
  }
});
