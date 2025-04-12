import prisma from '../../utils/prisma';
import { z, ZodError } from 'zod';
import { defineEventHandler, readBody, createError } from 'h3';
import { checkServerCreationRights } from '../../utils/serverPermissions';
import type { User } from '@prisma/client';
// Import enums for validation
import { ServerStatus, ServerAvailability, ServerType } from '@prisma/client'; // Ensure ServerType is imported

// Updated schema to include slug and type (using ServerType enum)
const createServerSchema = z.object({
  name: z.string().min(1, 'Name is required').max(100, 'Name must be 100 characters or less'),
  slug: z.string().min(1, 'Slug is required').regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/, 'Invalid slug format'),
  type: z.nativeEnum(ServerType), // Use the imported Prisma enum for validation
  description: z.string().max(500, "Description too long").optional().nullable(),
  website: z.string().url('Invalid URL format').optional().nullable(),
  email: z.string().email('Invalid email format').optional().nullable(),
  imageUrl: z.string().url('Invalid URL format').optional().nullable(),
  serverUrl: z.string().url('Server URL must be a valid URL'),
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
  let authenticatedUser: User;

  try {
    // 1. Check creation permissions
    ({ user: authenticatedUser } = await checkServerCreationRights(event));

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
          slug: validatedData.slug,
          type: validatedData.type, // Save validated type
          description: validatedData.description,
          website: validatedData.website,
          email: validatedData.email,
          imageUrl: validatedData.imageUrl,
          serverUrl: validatedData.serverUrl,
          // status and availability will use Prisma schema defaults
          // status: validatedData.status,
          // availability: validatedData.availability,
          owners: {
            create: [{ userId: authenticatedUser.id }],
          },
        },
        select: { // Select fields needed for response and navigation
          id: true,
          slug: true,
          name: true,
          description: true,
          website: true,
          email: true,
          imageUrl: true,
          serverUrl: true,
          status: true,
          availability: true,
          type: true, // Return type
          createdAt: true,
          updatedAt: true,
          owners: { select: { user: { select: { id: true, name: true, email: true } } } } // Return owner info
        }
      });

      // Create tools and parameters (unchanged)
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
      return server; // Return the created server data
    });

    // 4. Set status code and return response
    event.node.res.statusCode = 201;
    return newServer; // Return the created server data including the slug and type

  } catch (error: unknown) {
     console.error('Error creating server:', error);
     if (error instanceof ZodError || (error instanceof Error && 'statusCode' in error)) {
       throw error;
     }
      // Handle potential Prisma errors (e.g., unique constraint on slug)
     if (error instanceof Error && 'code' in error && error.code === 'P2002' && (error as any).meta?.target?.includes('slug')) {
         throw createError({ statusCode: 409, statusMessage: 'A server with this slug already exists.' });
     }
     throw createError({
       statusCode: 500,
       statusMessage: 'Failed to create server due to an unexpected error.',
     });
  }
});