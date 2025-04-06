// /home/alex/go-ai/gate4ai/www/server/api/servers/index.get.ts
import prisma from '../../utils/prisma';
import { defineEventHandler, createError, getQuery } from 'h3';
import type { Prisma, SubscriptionStatus, Server as _Server } from '@prisma/client'; // Combine imports and rename Server

// Define the structure of the public server data we want to RETURN
// Note: This is NOT a Prisma select object anymore
interface _PublicServerData {
  id: string;
  name: string;
  description: string | null;
  website: string | null;
  email: string | null;
  imageUrl: string | null;
  createdAt: Date;
  updatedAt: Date;
  tools: { id: string; name: string; description: string | null }[];
  _count: { tools: number; subscriptions: number };
}

// Define the fields Prisma needs to SELECT to build the public data
const publicServerSelectFields: Prisma.ServerSelect = {
  id: true,
  name: true,
  description: true,
  website: true,
  email: true,
  imageUrl: true,
  createdAt: true,
  updatedAt: true,
  tools: {
    select: {
      id: true,
      name: true,
      description: true,
    },
  },
  _count: {
    select: {
      tools: true,
      subscriptions: { where: { status: 'ACTIVE' } },
    },
  },
};

// GET - List all servers
export default defineEventHandler(async (event) => {
  try {
    const userId = event.context.user?.id;
    const { filter } = getQuery(event); // Get the 'filter' query parameter

    let prismaWhereClause: Prisma.ServerWhereInput = {}; // Initialize empty where clause

    // Build the WHERE clause based on the filter
    if (filter === 'owned') {
      if (!userId) {
         // Cannot filter by owned if user is not logged in
         return []; // Return empty array if user not logged in for 'owned' filter
      }
      prismaWhereClause = {
        owners: {
          some: { userId },
        },
      };
    } else if (filter === 'subscribed') {
       if (!userId) {
         // Cannot filter by subscribed if user is not logged in
         return []; // Return empty array if user not logged in for 'subscribed' filter
      }
      prismaWhereClause = {
        subscriptions: {
          some: { userId, status: 'ACTIVE' },
        },
      };
    }
    // Default: No filter applied, prismaWhereClause remains {}

    // Define what fields to select, including conditional ones for ownership/subscription checks
    const selectClause: Prisma.ServerSelect = {
      ...publicServerSelectFields, // Select all public fields
      // Conditionally include fields needed to determine ownership/subscription IF user is logged in
      ...(userId && {
        subscriptions: {
          where: { userId, status: 'ACTIVE' },
          select: { id: true, status: true }, // Select ID and STATUS
        },
        owners: {
          where: { userId },
          select: { userId: true }, // Only need existence check
        },
      }),
    };

    const servers = await prisma.server.findMany({
      where: prismaWhereClause,
      select: selectClause,
      orderBy: {
         name: 'asc',
       }
    });

    // Map the fetched Prisma data to the desired response structure
    return servers.map(server => {
      const typedServer = server as typeof server & { subscriptions?: {id: string, status: SubscriptionStatus}[], owners?: {id: string}[] };

      // Determine subscription status and ID for the current user
      let currentUserSubscriptionId: string | undefined = undefined;
      let isCurrentUserSubscribed = false;
      if (userId && typedServer.subscriptions && typedServer.subscriptions.length > 0) {
         // Usually there's only one subscription per user/server
         currentUserSubscriptionId = typedServer.subscriptions[0].id;
         isCurrentUserSubscribed = typedServer.subscriptions[0].status === 'ACTIVE'; // Check status
      }

      // Construct the response object
      const responseData = { // Define explicit type if needed
        id: typedServer.id,
        name: typedServer.name,
        description: typedServer.description,
        website: typedServer.website,
        email: typedServer.email,
        imageUrl: typedServer.imageUrl,
        createdAt: typedServer.createdAt,
        updatedAt: typedServer.updatedAt,
        tools: typedServer.tools,
        _count: typedServer._count,
        // Add calculated flags and ID
        isCurrentUserSubscribed: isCurrentUserSubscribed,
        isCurrentUserOwner: !!(userId && typedServer.owners && typedServer.owners.length > 0),
        subscriptionId: currentUserSubscriptionId, // Include the subscription ID
      };
      return responseData;
    });

  } catch (error) {
    console.error('Error fetching servers:', error);
    const errorMessage = error instanceof Error ? error.message : 'Unknown error occurred while fetching servers';
    console.error(`Server fetch error details: ${errorMessage}`);
    throw createError({
      statusCode: 500,
      statusMessage: 'Failed to fetch servers',
    });
  }
});