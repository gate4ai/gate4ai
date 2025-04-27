import { checkAuth } from "~/server/utils/userUtils";
import { defineEventHandler, createError } from "h3";
import prisma from "../../utils/prisma";

export default defineEventHandler(async (event) => {
  try {
    const currentUser = checkAuth(event);

    if (currentUser?.role !== "ADMIN") {
      throw createError({
        statusCode: 403,
        statusMessage: "Forbidden",
      });
    }

    // Get all settings
    const settings = await prisma.settings.findMany({
      orderBy: [{ group: "asc" }, { name: "asc" }],
    });

    return { settings };
  } catch (error) {
    console.error("Error fetching settings:", error);
    throw createError({
      statusCode: 500,
      statusMessage: "Error fetching settings",
    });
  }
});
