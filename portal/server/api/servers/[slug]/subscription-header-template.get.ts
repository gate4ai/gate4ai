import { defineEventHandler, getRouterParam, createError } from "h3";
import prisma from "../../../utils/prisma";
// No auth check needed here if template is considered public/subscriber-viewable

export default defineEventHandler(async (event) => {
  const slug = getRouterParam(event, "slug");
  if (!slug) {
    throw createError({
      statusCode: 400,
      statusMessage: "Server slug is required",
    });
  }

  try {
    // Fetch the server ID first based on slug
    const server = await prisma.server.findUnique({
      where: { slug },
      select: { id: true }, // Only need the ID
    });

    if (!server) {
      throw createError({ statusCode: 404, statusMessage: "Server not found" });
    }

    // Fetch the template using the server ID
    const template = await prisma.subscriptionHeaderTemplate.findMany({
      where: { serverId: server.id },
      select: {
        id: true, // Include ID if needed by frontend
        key: true,
        description: true,
        required: true,
      },
      orderBy: { key: "asc" }, // Optional: order by key
    });

    return template ?? []; // Return template or empty array
  } catch (error: unknown) {
    console.error(
      `Error fetching subscription header template for slug ${slug}:`,
      error
    );
    if (error instanceof Error && "statusCode" in error) {
      throw error; // Re-throw H3 errors (like 404)
    }
    throw createError({
      statusCode: 500,
      statusMessage: "Failed to fetch subscription header template",
    });
  }
});
