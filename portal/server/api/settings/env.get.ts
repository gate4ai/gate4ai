import { defineEventHandler, createError } from "h3";
import { checkAuth, isSecurityOrAdminUser } from "~/server/utils/userUtils";

export default defineEventHandler(async (event) => {
  const user = checkAuth(event); // Ensure user is authenticated

  // Authorization check: Only Admin or Security can access environment variables
  if (!isSecurityOrAdminUser(user)) {
    throw createError({
      statusCode: 403,
      statusMessage:
        "Forbidden: You do not have permission to view environment variables.",
    });
  }

  try {
    // Get environment variables from the Node.js process
    // WARNING: This exposes ALL environment variables, including potentially sensitive ones.
    // In a production scenario, consider filtering or redacting sensitive keys.
    // Admins/Security should already have high privileges, but caution is advised.
    const envVars = process.env;

    // Return as a key-value record
    // Ensure all values are strings for simplicity in frontend display
    const responseVars: Record<string, string> = {};
    for (const key in envVars) {
      // process.env values can be undefined, handle this case
      responseVars[key] = envVars[key] ?? "";
    }

    return responseVars;
  } catch (error: unknown) {
    console.error("Error fetching environment variables:", error);
    throw createError({
      statusCode: 500,
      statusMessage: "Failed to fetch environment variables.",
    });
  }
});
