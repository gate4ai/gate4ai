import {
  defineEventHandler,
  getRouterParam,
  createError,
  readMultipartFormData,
} from "h3";
import { checkServerModificationRights } from "../../../utils/serverPermissions";
import prisma from "../../../utils/prisma";
import fs from "node:fs/promises"; // Use promises API for async operations
import path from "node:path";

// Configuration
const MAX_FILE_SIZE_MB = 2;
const MAX_FILE_SIZE_BYTES = MAX_FILE_SIZE_MB * 1024 * 1024;
const ALLOWED_MIME_TYPES = [
  "image/png",
  "image/jpeg",
  "image/svg+xml",
  "image/gif",
];
const UPLOAD_DIR_RELATIVE = "public/uploads/servers"; // Relative to project root
const UPLOAD_DIR_FULL = path.resolve(process.cwd(), UPLOAD_DIR_RELATIVE); // Get absolute path

// Ensure upload directory exists on server startup
(async () => {
  try {
    await fs.mkdir(UPLOAD_DIR_FULL, { recursive: true });
    console.log(`Upload directory ensured: ${UPLOAD_DIR_FULL}`);
  } catch (err) {
    console.error(`Failed to ensure upload directory ${UPLOAD_DIR_FULL}:`, err);
  }
})();

// Allowed extensions mapping (lowercase)
const mimeToExt: Record<string, string> = {
  "image/png": "png",
  "image/jpeg": "jpg",
  "image/svg+xml": "svg",
  "image/gif": "gif",
};

export default defineEventHandler(async (event) => {
  const slug = getRouterParam(event, "slug");
  if (!slug) {
    throw createError({
      statusCode: 400,
      statusMessage: "Server slug is required",
    });
  }

  try {
    // 1. Check Permissions (Owner/Admin/Security)
    const { server } = await checkServerModificationRights(event, slug);

    // 2. Read multipart form data
    const formData = await readMultipartFormData(event);

    // --- DEBUGGING: Log the received form data structure ---
    console.log(
      `[${slug}/logo.post] Received formData parts:`,
      formData?.map((p) => ({
        name: p.name,
        filename: p.filename,
        type: p.type,
        size: p.data?.length,
      }))
    );

    if (!formData) {
      throw createError({
        statusCode: 400,
        statusMessage: "No form data received.",
      });
    }

    // Find the file part named 'logoFile'
    const logoFile = formData.find((part) => part.name === "logoFile");

    // --- DEBUGGING: Log the found logoFile part ---
    console.log(
      `[${slug}/logo.post] Found 'logoFile' part details:`,
      logoFile
        ? {
            name: logoFile.name,
            filename: logoFile.filename,
            type: logoFile.type,
            hasData: !!logoFile.data,
            dataLength: logoFile.data?.length,
          }
        : "Part Not Found"
    );

    // 3. Validate the found file part more carefully
    if (!logoFile) {
      throw createError({
        statusCode: 400,
        statusMessage: "Missing 'logoFile' part in the form data.",
      });
    }
    // Check essential properties *after* confirming logoFile exists
    if (!logoFile.filename) {
      throw createError({
        statusCode: 400,
        statusMessage: "Uploaded file is missing a filename.",
      });
    }
    if (!logoFile.type) {
      throw createError({
        statusCode: 400,
        statusMessage: "Uploaded file is missing a content type.",
      });
    }
    // Check if data exists and has length > 0
    if (!logoFile.data || logoFile.data.length === 0) {
      throw createError({
        statusCode: 400,
        statusMessage: "Uploaded file data is empty.",
      });
    }

    // 4. Validate File Content Type and Size
    if (!ALLOWED_MIME_TYPES.includes(logoFile.type)) {
      throw createError({
        statusCode: 400,
        statusMessage: `Invalid file type '${
          logoFile.type
        }'. Allowed types: ${Object.values(mimeToExt).join(", ")}.`,
      });
    }
    if (logoFile.data.length > MAX_FILE_SIZE_BYTES) {
      throw createError({
        statusCode: 400,
        statusMessage: `File is too large (${(
          logoFile.data.length /
          1024 /
          1024
        ).toFixed(2)}MB). Maximum size is ${MAX_FILE_SIZE_MB}MB.`,
      });
    }

    // 5. Determine Extension and Filename
    const extension = mimeToExt[logoFile.type];
    if (!extension) {
      // Should not happen if ALLOWED_MIME_TYPES is correct, but good practice
      throw createError({
        statusCode: 500,
        statusMessage:
          "Internal server error: Could not determine file extension from allowed type.",
      });
    }
    const filename = `${slug}-logo.${extension}`;
    const filePath = path.join(UPLOAD_DIR_FULL, filename);

    // 6. Save File (Overwrites existing)
    try {
      await fs.writeFile(filePath, logoFile.data);
      console.log(`[${slug}/logo.post] Logo saved to ${filePath}`);
    } catch (writeError) {
      console.error(
        `[${slug}/logo.post] Error writing file to ${filePath}:`,
        writeError
      );
      throw createError({
        statusCode: 500,
        statusMessage: "Failed to save uploaded file.",
      });
    }

    // 7. Generate Relative URL for DB
    const imageUrl = `/uploads/servers/${filename}`; // Relative path for client access

    // 8. Update Database
    try {
      await prisma.server.update({
        where: { id: server.id }, // Use the actual ID (UUID) from permission check
        data: {
          imageUrl: imageUrl,
        },
      });
      console.log(
        `[${slug}/logo.post] Database updated with imageUrl: ${imageUrl}`
      );
    } catch (dbError) {
      console.error(
        `[${slug}/logo.post] Error updating database for server ${server.id}:`,
        dbError
      );
      // Attempt to clean up saved file if DB update fails? Optional.
      try {
        await fs.unlink(filePath);
      } catch (cleanupErr) {
        console.error(
          `[${slug}/logo.post] Failed to clean up file ${filePath} after DB error:`,
          cleanupErr
        );
      }
      throw createError({
        statusCode: 500,
        statusMessage: "Failed to update server record with new logo URL.",
      });
    }

    // 9. Return Success Response
    return { imageUrl }; // Return the new URL
  } catch (error: unknown) {
    // Log the specific error caught before potentially re-throwing
    console.error(
      `[${slug}/logo.post] Error during logo upload process:`,
      error
    );
    if (error instanceof Error && "statusCode" in error) {
      // If it's already an H3 error (like 400, 403, 404 from earlier checks), just throw it
      throw error;
    }
    // Otherwise, wrap it in a generic 500 server error
    throw createError({
      statusCode: 500,
      statusMessage: "Failed to upload logo due to an unexpected server error.",
    });
  }
  // Prisma disconnects automatically in Nitro based environment
});
