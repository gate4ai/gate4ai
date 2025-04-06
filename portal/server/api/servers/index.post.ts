// /home/alex/go-ai/gate4ai/www/server/api/servers/index.post.ts
import prisma from '../../utils/prisma';
import { z, ZodError } from 'zod';
import { defineEventHandler, readBody, createError } from 'h3';
import { checkServerCreationRights } from '../../utils/serverPermissions';
import type { User } from '@prisma/client';
import  { ServerStatus, ServerAvailability } from '@prisma/client';

// Schema definition remains the same
const createServerSchema = z.object({
  name: z.string().min(1, 'Name is required').max(100, 'Name must be 100 characters or less'),
  description: z.string().max(500, "Description too long").optional().nullable(),
  website: z.string().url('Invalid URL format').optional().nullable(),
  email: z.string().email('Invalid email format').optional().nullable(),
  imageUrl: z.string().url('Invalid URL format').optional().nullable(),
  serverUrl: z.string().url('Server URL must be a valid URL'),
  status: z.nativeEnum(ServerStatus).optional().default('DRAFT'),
  availability: z.nativeEnum(ServerAvailability).optional().default('SUBSCRIPTION'),
  tools: z.array(
    z.object({
      name: z.string().min(1, 'Tool name is required'),
      description: z.string().optional().nullable(),
      parameters: z.array(
        z.object({
          name: z.string().min(1, 'Parameter name is required'),
          type: z.string().min(1, 'Parameter type is required'),
          description: z.string().optional().nullable(),
          required: z.boolean().optional().default(false)
        })
      ).optional().default([])
    })
  ).optional().default([])
}).strict();

export default defineEventHandler(async (event) => {
  let authenticatedUser: User; // Changed: Ensure user is assigned

  try {
    // 1. Check creation permissions. Throws if not authorized.
    // Returns the authenticated user if successful.
    // Destructure user directly
    ({ user: authenticatedUser } = await checkServerCreationRights(event)); // Assign user here

    // 2. Read and validate request body
    const body = await readBody(event);
    const validationResult = createServerSchema.safeParse(body);

    if (!validationResult.success) {
      throw createError({
        statusCode: 400,
        statusMessage: 'Validation Error',
        data: validationResult.error.flatten().fieldErrors
      });
    }
    const validatedData = validationResult.data;

    // 3. Create server with tools and parameters in a transaction
    const newServer = await prisma.$transaction(async (tx) => {
      const server = await tx.server.create({
        data: {
          name: validatedData.name,
          description: validatedData.description,
          website: validatedData.website,
          email: validatedData.email,
          imageUrl: validatedData.imageUrl,
          serverUrl: validatedData.serverUrl,
          status: validatedData.status,
          availability: validatedData.availability,
          owners: {
            create: [{ userId: authenticatedUser.id }],
          },
        },
        select: {
          id: true,
          name: true,
          description: true,
          website: true,
          email: true,
          imageUrl: true,
          serverUrl: true,
          status: true,
          availability: true,
          createdAt: true,
          updatedAt: true,
          owners: { select: { user: true } }
        }
      });

      // Create tools and parameters (logic remains the same)
      if (validatedData.tools && validatedData.tools.length > 0) {
        for (const toolData of validatedData.tools) {
          const newTool = await tx.tool.create({
            data: {
              name: toolData.name,
              description: toolData.description,
              serverId: server.id
            },
            select: { id: true }
          });

          if (toolData.parameters && toolData.parameters.length > 0) {
            await tx.toolParameter.createMany({
              data: toolData.parameters.map(param => ({
                name: param.name,
                type: param.type,
                description: param.description,
                required: param.required,
                toolId: newTool.id
              }))
            });
          }
        }
      }
      return server; // Return the created server data from the transaction
    });

    // 4. Set status code for successful creation
    event.node.res.statusCode = 201;
    return newServer;

  } catch (error: unknown) {
     console.error('Error creating server:', error);
     if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) {
       throw error;
     }
     throw createError({
       statusCode: 500,
       statusMessage: 'Failed to create server due to an unexpected error.',
     });
  }
});