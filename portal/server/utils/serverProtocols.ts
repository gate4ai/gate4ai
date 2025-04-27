/**
 * Utilities for handling protocol-specific data conversion between UI models and database models
 */
import type { AgentSkill, RestEndpoint } from "~/utils/server";

// Database model type definitions (for compatibility before Prisma generates them)
interface DbA2ASkill {
  id: string;
  name: string;
  description: string | null;
  tags: string[];
  examples: string[];
  inputModes: string[];
  outputModes: string[];
  serverId: string;
}

interface DbRESTEndpoint {
  id: string;
  path: string;
  method: string;
  description: string | null;
  serverId: string;
  parameters: DbRESTParameter[];
  requestBody: DbRESTRequestBody | null;
  responses: DbRESTResponse[];
}

interface DbRESTParameter {
  id: string;
  name: string;
  type: string;
  description: string | null;
  required: boolean;
  endpointId: string;
}

interface DbRESTRequestBody {
  id: string;
  description: string | null;
  example: string | null;
  endpointId: string;
}

interface DbRESTResponse {
  id: string;
  statusCode: number;
  description: string;
  example: string | null;
  endpointId: string;
}

// Prisma input type definitions (for compatibility before Prisma generates them)
interface A2ASkillCreateInput {
  name: string;
  description?: string | null;
  tags: string[];
  examples: string[];
  inputModes: string[];
  outputModes: string[];
  server: {
    connect: { id: string };
  };
}

interface RESTEndpointCreateInput {
  path: string;
  method: string;
  description?: string | null;
  server: {
    connect: { id: string };
  };
  parameters?: {
    create: {
      name: string;
      type: string;
      description?: string | null;
      required: boolean;
    }[];
  };
  requestBody?: {
    create: {
      description?: string | null;
      example?: string | null;
    };
  };
  responses?: {
    create: {
      statusCode: number;
      description: string;
      example?: string | null;
    }[];
  };
}

/**
 * Converts a database A2A skill to API format
 * @param dbSkill Skill from database
 * @returns API-formatted skill
 */
export function mapDbA2ASkillToApiSkill(dbSkill: DbA2ASkill): AgentSkill {
  return {
    id: dbSkill.id,
    name: dbSkill.name,
    description: dbSkill.description,
    tags: dbSkill.tags,
    examples: dbSkill.examples,
    inputModes: dbSkill.inputModes,
    outputModes: dbSkill.outputModes,
  };
}

/**
 * Converts an API A2A skill to database format
 * @param apiSkill Skill from API
 * @param serverId Server ID to associate with
 * @returns Database create input for the skill
 */
export function mapApiSkillToDbCreateInput(
  apiSkill: AgentSkill,
  serverId: string
): A2ASkillCreateInput {
  return {
    name: apiSkill.name,
    description: apiSkill.description,
    tags: apiSkill.tags || [],
    examples: apiSkill.examples || [],
    inputModes: apiSkill.inputModes || ["text"],
    outputModes: apiSkill.outputModes || ["text"],
    server: {
      connect: { id: serverId },
    },
  };
}

/**
 * Maps a full REST endpoint from the database (with relations) to API format
 * @param dbEndpoint Endpoint from database with parameters, request body and responses
 * @returns API-formatted endpoint
 */
export function mapDbRestEndpointToApiEndpoint(
  dbEndpoint: DbRESTEndpoint
): RestEndpoint {
  return {
    path: dbEndpoint.path,
    method: dbEndpoint.method,
    description: dbEndpoint.description,
    queryParams: dbEndpoint.parameters.map((param) => ({
      name: param.name,
      type: param.type,
      description: param.description,
      required: param.required,
    })),
    requestBody: dbEndpoint.requestBody
      ? {
          description: dbEndpoint.requestBody.description,
          example: dbEndpoint.requestBody.example,
        }
      : undefined,
    responses: dbEndpoint.responses.map((response) => ({
      statusCode: response.statusCode,
      description: response.description,
      example: response.example,
    })),
  };
}

/**
 * Prepares database create input for a REST endpoint with all its related entities
 * @param apiEndpoint Endpoint from API
 * @param serverId Server ID to associate with
 * @returns Database create input for the endpoint and its relations
 */
export function mapApiEndpointToDbCreateInput(
  apiEndpoint: RestEndpoint,
  serverId: string
): RESTEndpointCreateInput {
  return {
    path: apiEndpoint.path,
    method: apiEndpoint.method,
    description: apiEndpoint.description,
    server: {
      connect: { id: serverId },
    },
    parameters: {
      create:
        apiEndpoint.queryParams?.map((param) => ({
          name: param.name,
          type: param.type,
          description: param.description,
          required: param.required,
        })) || [],
    },
    requestBody: apiEndpoint.requestBody
      ? {
          create: {
            description: apiEndpoint.requestBody.description,
            example: apiEndpoint.requestBody.example,
          },
        }
      : undefined,
    responses: {
      create:
        apiEndpoint.responses?.map((response) => ({
          statusCode: response.statusCode,
          description: response.description,
          example: response.example,
        })) || [],
    },
  };
}
