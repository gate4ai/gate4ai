// /home/alex/go-ai/gate4ai/www/utils/server.ts
/**
 * Shared server and tool interfaces
 */
import type { SubscriptionStatus, ServerStatus as PrismaServerStatus, ServerAvailability as PrismaServerAvailability } from '@prisma/client';

// Basic tool information remains the same
export interface ToolInfo {
  id: string;
  name: string;
}

// Detailed tool information with schema remains the same
export interface Tool extends ToolInfo {
  description?: string; // Make optional
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
  // Update: Ensure parameters match the Tool definition from backend if nested differently
  parameters: ServerParameter[];
}

// Server owner information remains the same
export interface ServerOwner {
  user: {
    id: string;
    name?: string; // Optional based on Prisma select
    email: string; // Assume email is usually selected
  }
}

// Basic server information - Used in lists/cards
export interface ServerInfo {
  id: string;
  name: string;
  description: string | null; // Match Prisma optionality
  imageUrl: string | null; // Match Prisma optionality
  website?: string | null; // Often included in basic info
  email?: string | null;   // Often included in basic info
  createdAt: string; // Dates are usually strings after JSON serialization
  updatedAt: string;
  tools: ToolInfo[]; // Keep basic tool info for lists
  _count?: {
    tools: number;
    subscriptions: number; // Active subscriptions count
  };
  // Flags added by API based on context
  isCurrentUserSubscribed?: boolean;
  isCurrentUserOwner?: boolean;
  subscriptionId?: string; // ID of the current user's subscription, if subscribed
}

// Complete server information - Used for detailed view ([id].vue)
// Extends ServerInfo and adds fields for detailed/owner view
export interface Server extends ServerInfo {
  status: PrismaServerStatus; // Use imported enum type
  availability: PrismaServerAvailability; // Use imported enum type
  serverUrl: string; // URL should be present in detailed view for owners/admins
  // tools should be more detailed in the full Server view
  tools: ServerTool[]; // Use ServerTool with detailed parameters
  owners: ServerOwner[]; // List of owners (should always be present, maybe empty)
  // Counts grouped by status (present for users with extended access)
  subscriptionStatusCounts?: Record<SubscriptionStatus, number>;
  // _count might be redundant if subscriptionStatusCounts is present, but keep for consistency if API returns both
}

// Server data for forms (remains largely the same)
export interface ServerData {
  id?: string; // Include ID for edit forms
  name: string;
  description?: string | null;
  website?: string | null;
  email?: string | null;
  imageUrl?: string | null;
  serverUrl: string; // Required for form
  status: PrismaServerStatus; // Required for form
  availability: PrismaServerAvailability; // Required for form
  // Tools are usually handled separately, not directly in the main form data object
}