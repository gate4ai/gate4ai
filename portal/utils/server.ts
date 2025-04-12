// gate4ai/portal/utils/server.ts
/**
 * Shared server and tool interfaces
 */
// Import necessary enums directly from Prisma types if possible,
// otherwise redeclare them here based on the schema definition.
// Assuming Prisma enums are available:
import type { SubscriptionStatus, ServerStatus, ServerAvailability, ServerType as PrismaServerType } from '@prisma/client';

// Export the ServerType enum for use in components
export type ServerType = PrismaServerType;

// Basic tool information remains the same
export interface ToolInfo {
  id: string;
  name: string;
}

// Detailed tool information with schema remains the same
export interface Tool extends ToolInfo {
  description?: string;
  inputSchema?: InputSchema;
}

// Parameter schema for tool inputs remains the same
export interface ParameterSchema {
  type: string;
  description?: string;
  enum?: string[];
  default?: unknown;
}

// Input schema for tool parameters remains the same
export interface InputSchema {
  type: string;
  properties: Record<string, ParameterSchema>;
  required?: string[];
}

// Detailed parameter information for server tools remains the same
export interface ServerParameter {
  id: string;
  name: string;
  type: string;
  description?: string; // Make optional
  required?: boolean;
}

// Detailed tool information for server display
export interface ServerTool {
  id: string;
  name: string;
  description?: string; // Make optional
  parameters: ServerParameter[];
}

// Server owner information remains the same
export interface ServerOwner {
  user: {
    id: string;
    name?: string | null; // Allow null from Prisma
    email: string; // Assume email is usually selected
  }
}

// Basic server information - Used in lists/cards
export interface ServerInfo {
  id: string;
  slug: string;
  type: ServerType; // Use Prisma enum
  name: string;
  description: string | null;
  imageUrl: string | null;
  website?: string | null;
  email?: string | null;
  createdAt: string; // Keep as string for simplicity or use Date
  updatedAt: string; // Keep as string
  tools: ToolInfo[]; // Use basic ToolInfo for lists
  _count?: {
    tools: number;
    subscriptions: number; // Active subscriptions count
  };
  // Flags added by API based on context
  isCurrentUserSubscribed?: boolean;
  isCurrentUserOwner?: boolean;
  subscriptionId?: string;
}

// Complete server information - Used for detailed view ([slug].vue)
export interface Server extends ServerInfo {
  status: ServerStatus; // Use imported enum type
  availability: ServerAvailability; // Use imported enum type
  serverUrl: string;
  tools: ServerTool[]; // Use ServerTool with detailed parameters
  owners: ServerOwner[];
  subscriptionStatusCounts?: Record<SubscriptionStatus, number>;
}

// Server data for forms (matches ServerFormData in ServerForm.vue)
export interface ServerData {
  id?: string; // Optional for create, required for edit
  slug: string;
  type: ServerType; // Use Prisma enum
  name: string;
  description?: string | null;
  website?: string | null;
  email?: string | null;
  imageUrl?: string | null;
  serverUrl: string;
  status: ServerStatus; // Use Prisma type
  availability: ServerAvailability; // Use Prisma type
  // Tools are usually handled separately, not directly in the main form data object
}