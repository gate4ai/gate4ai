import {
  defineEventHandler,
  sendStream,
  createError,
  setResponseHeader,
  getRequestURL,
} from "h3";
import fs, { stat } from "node:fs/promises";
import { createReadStream } from "node:fs";
import { resolve, extname, normalize } from "node:path";
import { lookup } from "mime-types"; // Utility to get MIME type from extension

// Define the absolute path to the PARENT 'uploads' directory on the server
const UPLOAD_DIR = resolve(process.cwd(), "public/uploads");
// console.log(`[uploads middleware] Serving static files from: ${UPLOAD_DIR}`);

// Ensure upload directory exists on server startup
(async () => {
  try {
    await fs.mkdir(UPLOAD_DIR, { recursive: true });
    // console.log(`Upload directory ensured: ${UPLOAD_DIR}`);
  } catch (err) {
    console.error(`Failed to ensure upload directory ${UPLOAD_DIR}:`, err);
  }
})();

export default defineEventHandler(async (event) => {
  const url = getRequestURL(event); // Get the URL object

  // --- ADD THIS CHECK ---
  // Only proceed if the path explicitly starts with /uploads/
  if (!url.pathname.startsWith("/uploads/")) {
    // console.log(`[uploads middleware] Path ${url.pathname} does not match /uploads/, skipping.`);
    return; // Pass through to the next handler without interfering
  }
  // --- END CHECK ---

  // Now, we know the path *should* be handled by this middleware.
  // Extract the path *relative* to /uploads/
  // Example: if pathname is /uploads/servers/logo.png, requestedPath will be servers/logo.png
  const requestedPath = url.pathname.substring("/uploads/".length);

  // Basic security checks
  if (!requestedPath) {
    // This case might happen if the request is just "/uploads/"
    // Depending on requirements, you might serve an index or return 400/404
    console.warn(
      "[uploads middleware] Request for base /uploads/ directory or invalid path."
    );
    throw createError({ statusCode: 400, message: "Invalid request path" });
  }
  // Prevent path traversal (ensure path doesn't go outside UPLOAD_DIR)
  const safePathSuffix = normalize(requestedPath).replace(
    /^(\.\.(\/|\\|$))+/,
    ""
  );
  if (safePathSuffix.includes("..")) {
    console.warn(
      `[uploads middleware] Blocked potentially unsafe path: ${requestedPath}`
    );
    throw createError({ statusCode: 400, message: "Invalid request path" });
  }

  // Construct the full, absolute path to the requested file
  const filePath = resolve(UPLOAD_DIR, safePathSuffix);

  // Double-check the file path is still within the intended directory
  if (!filePath.startsWith(UPLOAD_DIR)) {
    console.warn(
      `[uploads middleware] Blocked path escaping uploads directory: ${filePath}`
    );
    throw createError({ statusCode: 403, message: "Forbidden" });
  }

  try {
    // Check if the file exists and is accessible
    const fileStat = await stat(filePath);

    // Ensure it's a file, not a directory
    if (!fileStat.isFile()) {
      // console.warn(`[uploads middleware] Requested path is not a file: ${filePath}`);
      throw createError({ statusCode: 404, message: "Not found" });
    }

    // Determine content type based on file extension
    const contentType = lookup(extname(filePath)); // Use lookup function
    if (contentType) {
      setResponseHeader(event, "Content-Type", contentType);
    } else {
      setResponseHeader(event, "Content-Type", "application/octet-stream"); // Default fallback
    }

    // Set cache headers (optional, but recommended for images)
    setResponseHeader(event, "Cache-Control", "public, max-age=3600"); // Cache for 1 hour

    // Stream the file content
    // console.log(`[uploads middleware] Serving file: ${filePath} with type ${contentType}`);
    return sendStream(event, await createReadStream(filePath));
  } catch (error: any) {
    // Handle file not found specifically
    if (error.code === "ENOENT") {
      // console.warn(`[uploads middleware] File not found: ${filePath}`);
      throw createError({ statusCode: 404, message: "File not found" });
    }
    // Handle other errors (e.g., permissions)
    console.error(
      `[uploads middleware] Error serving file ${filePath}:`,
      error
    );
    throw createError({ statusCode: 500, message: "Error serving file" });
  }
});
