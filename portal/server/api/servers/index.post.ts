import prisma from "../../utils/prisma";
import { z, ZodError } from "zod";
import { defineEventHandler, readBody, createError } from "h3";
import { checkServerCreationRights } from "../../utils/serverPermissions";
import type { User } from "@prisma/client";
// Import enums for validation
import { ServerProtocol } from "@prisma/client"; // Use ServerProtocol instead of ServerType
import { Prisma } from "@prisma/client";

// --- Reusable Schemas ---
const parameterSchema = z.object({
  name: z.string().min(1, "Parameter name is required"),
  type: z.string().min(1, "Parameter type is required"),
  description: z.string().optional().nullable(),
  required: z.boolean().optional().default(false),
});

const toolSchema = z.object({
  name: z.string().min(1, "Tool name is required"),
  description: z.string().optional().nullable(),
  parameters: z.array(parameterSchema).optional().default([]),
});

const skillSchema = z.object({
  id: z.string().min(1, "Skill ID is required"), // Keep ID from discovery
  name: z.string().min(1, "Skill name is required"),
  description: z.string().optional().nullable(),
  tags: z.array(z.string()).optional().default([]),
  examples: z.array(z.string()).optional().default([]),
  inputModes: z.array(z.string()).optional().default(["text"]),
  outputModes: z.array(z.string()).optional().default(["text"]),
});

const responseSchema = z.object({
  statusCode: z.number().int().min(100).max(599),
  description: z.string().min(1, "Response description is required"),
  example: z.string().optional().nullable(),
});

const requestBodySchema = z.object({
  description: z.string().optional().nullable(),
  example: z.string().optional().nullable(),
});

const endpointSchema = z.object({
  path: z.string().min(1, "Endpoint path is required"),
  method: z.string().min(1, "HTTP method is required"),
  description: z.string().optional().nullable(),
  queryParams: z.array(parameterSchema).optional().default([]),
  requestBody: requestBodySchema.optional().nullable(),
  responses: z.array(responseSchema).optional().default([]),
});

// Schema for server-specific headers (optional map of string to string)
const serverHeadersSchema = z
  .record(z.string(), z.string())
  .optional()
  .nullable();

// Schema for subscription header template item (optional array)
const subscriptionHeaderTemplateItemSchema = z
  .object({
    key: z
      .string()
      .min(1, "Header key cannot be empty")
      .regex(/^[A-Za-z0-9-]+$/, "Invalid header key format"),
    description: z.string().optional().nullable(),
    required: z.boolean().optional().default(false),
  })
  .strict();
const subscriptionHeaderTemplateSchema = z
  .array(subscriptionHeaderTemplateItemSchema)
  .optional()
  .default([]);

// --- Main Create Server Schema ---
const createServerSchema = z
  .object({
    name: z
      .string()
      .min(1, "Name is required")
      .max(100, "Name must be 100 characters or less"),
    slug: z
      .string()
      .min(1, "Slug is required")
      .regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/, "Invalid slug format"),
    protocol: z.nativeEnum(ServerProtocol), // Use ServerProtocol enum
    description: z
      .string()
      .max(500, "Description too long")
      .optional()
      .nullable(),
    website: z.string().url("Invalid URL format").optional().nullable(),
    email: z.string().email("Invalid email format").optional().nullable(),
    imageUrl: z.string().url("Invalid URL format").optional().nullable(),
    serverUrl: z.string().url("Server URL must be a valid URL"),
    protocolVersion: z.string().optional().nullable(), // Make nullable
    tools: z.array(toolSchema).optional().default([]), // MCP Tools
    a2aSkills: z.array(skillSchema).optional().default([]), // A2A Skills
    restEndpoints: z.array(endpointSchema).optional().default([]), // REST Endpoints
    headers: serverHeadersSchema, // Server headers
    subscriptionHeaderTemplate: subscriptionHeaderTemplateSchema, // Subscription template
  })
  .strict();

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
        statusMessage: "Validation Error",
        data: validationResult.error.flatten().fieldErrors,
      });
    }
    const validatedData = validationResult.data;

    // 3. Create server with related data in a transaction
    const newServer = await prisma.$transaction(async (tx) => {
      // Create the base server
      const server = await tx.server.create({
        data: {
          name: validatedData.name,
          slug: validatedData.slug,
          protocol: validatedData.protocol as ServerProtocol,
          description: validatedData.description,
          website: validatedData.website,
          email: validatedData.email,
          imageUrl: validatedData.imageUrl,
          serverUrl: validatedData.serverUrl,
          protocolVersion: validatedData.protocolVersion,
          headers:
            (validatedData.headers as Prisma.JsonObject) ?? Prisma.JsonNull, // Save headers
          // status and availability will use Prisma schema defaults
          owners: {
            create: [{ userId: authenticatedUser.id }],
          },
          // Create template items if provided
          ...(validatedData.subscriptionHeaderTemplate &&
            validatedData.subscriptionHeaderTemplate.length > 0 && {
              subscriptionHeaderTemplate: {
                createMany: {
                  data: validatedData.subscriptionHeaderTemplate.map(
                    (item) => ({
                      key: item.key,
                      description: item.description,
                      required: item.required,
                    })
                  ),
                },
              },
            }),
        },
        select: {
          // Select fields needed for response and navigation
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
          protocol: true,
          protocolVersion: true,
          createdAt: true,
          updatedAt: true,
          owners: {
            select: { user: { select: { id: true, name: true, email: true } } },
          },
        },
      });

      // Create MCP tools and parameters if protocol is MCP
      if (
        validatedData.protocol === "MCP" &&
        validatedData.tools &&
        validatedData.tools.length > 0
      ) {
        for (const toolData of validatedData.tools) {
          const newTool = await tx.tool.create({
            data: {
              name: toolData.name,
              description: toolData.description,
              serverId: server.id,
            },
            select: { id: true },
          });

          if (toolData.parameters && toolData.parameters.length > 0) {
            await tx.toolParameter.createMany({
              data: toolData.parameters.map((param) => ({
                name: param.name,
                type: param.type,
                description: param.description,
                required: param.required,
                toolId: newTool.id,
              })),
            });
          }
        }
      }

      // Create A2A skills if protocol is A2A
      if (
        validatedData.protocol === "A2A" &&
        validatedData.a2aSkills &&
        validatedData.a2aSkills.length > 0
      ) {
        await tx.a2ASkill.createMany({
          data: validatedData.a2aSkills.map((skill) => ({
            name: skill.name,
            description: skill.description,
            tags: skill.tags,
            examples: skill.examples,
            inputModes: skill.inputModes,
            outputModes: skill.outputModes,
            serverId: server.id,
          })),
        });
      }

      // Create REST endpoints if protocol is REST
      if (
        validatedData.protocol === "REST" &&
        validatedData.restEndpoints &&
        validatedData.restEndpoints.length > 0
      ) {
        for (const endpointData of validatedData.restEndpoints) {
          const newEndpoint = await tx.rESTEndpoint.create({
            data: {
              path: endpointData.path,
              method: endpointData.method,
              description: endpointData.description,
              serverId: server.id,
            },
            select: { id: true },
          });

          if (endpointData.queryParams && endpointData.queryParams.length > 0) {
            await tx.rESTParameter.createMany({
              data: endpointData.queryParams.map((param) => ({
                name: param.name,
                type: param.type,
                description: param.description,
                required: param.required,
                endpointId: newEndpoint.id,
              })),
            });
          }

          if (endpointData.requestBody) {
            await tx.rESTRequestBody.create({
              data: {
                description: endpointData.requestBody.description,
                example: endpointData.requestBody.example,
                endpointId: newEndpoint.id,
              },
            });
          }

          if (endpointData.responses && endpointData.responses.length > 0) {
            await tx.rESTResponse.createMany({
              data: endpointData.responses.map((response) => ({
                statusCode: response.statusCode,
                description: response.description,
                example: response.example,
                endpointId: newEndpoint.id,
              })),
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
    console.error("Error creating server:", error);
    if (
      error instanceof ZodError ||
      (error instanceof Error && "statusCode" in error)
    ) {
      throw error;
    }
    // Handle potential Prisma errors (e.g., unique constraint on slug)
    if (
      error instanceof Error &&
      "code" in error &&
      (error as { code: string }).code === "P2002" &&
      (error as { meta?: { target?: string[] } }).meta?.target?.includes("slug")
    ) {
      throw createError({
        statusCode: 409,
        statusMessage: "A server with this slug already exists.",
      });
    }
    throw createError({
      statusCode: 500,
      statusMessage: "Failed to create server due to an unexpected error.",
    });
  }
});
