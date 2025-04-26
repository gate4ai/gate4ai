import { z } from "zod";
import { checkAuth, checkUserPermissions } from "~/server/utils/userUtils";
import prisma from "../../utils/prisma";
import { defineEventHandler, readBody, createError } from "h3";
import bcrypt from "bcrypt"; // Import bcrypt for password handling
import { Role, Status } from "@prisma/client"; // Import enums for validation

// Base schema for common fields
const baseUserSchema = z.object({
  name: z.string().min(1, "Name cannot be empty").optional(),
  company: z.string().nullable().optional(), // Allow null for company
});

// Schema for users updating their own profile, includes password fields
const selfUpdateSchema = baseUserSchema
  .extend({
    currentPassword: z.string().optional(),
    newPassword: z
      .string()
      .min(8, "New password must be at least 8 characters")
      .optional(),
  })
  .refine(
    (data) => {
      // If newPassword is provided, currentPassword must also be provided
      if (data.newPassword && !data.currentPassword) {
        return false;
      }
      return true;
    },
    {
      message: "Current password is required to set a new password",
      path: ["currentPassword"], // Field to associate the error with
    }
  );

// Schema for admins updating any user profile (no password fields allowed)
const adminUpdateSchema = baseUserSchema.extend({
  role: z.nativeEnum(Role).optional(), // Use nativeEnum for Prisma enums
  status: z.nativeEnum(Status).optional(), // Use nativeEnum for Prisma enums
  comment: z.string().nullable().optional(), // Allow null for comment
});

// Define inferred types for clarity
type SelfUpdateData = z.infer<typeof selfUpdateSchema>;
type AdminUpdateData = z.infer<typeof adminUpdateSchema>;

export default defineEventHandler(async (event) => {
  // Check if user is authenticated
  const currentUser = checkAuth(event);

  const params = event.context.params || {};
  const targetUserId = params.id;

  if (!targetUserId) {
    throw createError({
      statusCode: 400,
      statusMessage: "User ID is required in the URL path",
    });
  }

  // Check permissions: Is the current user updating themselves or are they an admin?
  const { isSelfUpdate, hasAdminAccess } = checkUserPermissions(
    currentUser,
    targetUserId
  );

  try {
    const body = await readBody(event);
    // Use a more generic type initially for the payload
    const updatePayload: Record<string, unknown> = {};

    if (isSelfUpdate) {
      // User is updating their own profile
      const validationResult = selfUpdateSchema.safeParse(body);
      if (!validationResult.success) {
        throw createError({
          statusCode: 400,
          statusMessage: "Validation Error",
          data: validationResult.error.flatten().fieldErrors,
        });
      }
      // Assign to a specifically typed variable
      const selfData: SelfUpdateData = validationResult.data;

      // Populate basic fields for update
      if (selfData.name !== undefined) updatePayload.name = selfData.name;
      if (selfData.company !== undefined)
        updatePayload.company = selfData.company;

      // --- Password Change Logic ---
      // Use the specifically typed variable 'selfData' here
      if (selfData.currentPassword && selfData.newPassword) {
        // Fetch user with password hash ONLY if changing password
        const userWithPassword = await prisma.user.findUnique({
          where: { id: targetUserId },
          select: { password: true },
        });

        if (!userWithPassword || !userWithPassword.password) {
          // Should not happen if user is authenticated, but good practice
          throw createError({
            statusCode: 404,
            statusMessage: "User data not found for password verification.",
          });
        }

        // Verify current password
        const isPasswordValid = await bcrypt.compare(
          selfData.currentPassword,
          userWithPassword.password
        );
        if (!isPasswordValid) {
          throw createError({
            statusCode: 401,
            statusMessage: "Incorrect current password.",
          });
        }

        // Hash the new password
        const hashedNewPassword = await bcrypt.hash(selfData.newPassword, 10);
        updatePayload.password = hashedNewPassword; // Add hashed password to payload
        console.log(`Password updated for user ${targetUserId}`);
      } else if (selfData.currentPassword || selfData.newPassword) {
        // Handle cases where only one password field is sent
        throw createError({
          statusCode: 400,
          statusMessage:
            "Both current and new password must be provided to change the password.",
        });
      }
      // --- End Password Change Logic ---
    } else if (hasAdminAccess) {
      // Admin is updating another user's profile
      const validationResult = adminUpdateSchema.safeParse(body);
      if (!validationResult.success) {
        throw createError({
          statusCode: 400,
          statusMessage: "Validation Error",
          data: validationResult.error.flatten().fieldErrors,
        });
      }
      // Assign to a specifically typed variable
      const adminData: AdminUpdateData = validationResult.data;

      // Populate fields admins can change
      // Use the specifically typed variable 'adminData' here
      if (adminData.name !== undefined) updatePayload.name = adminData.name;
      if (adminData.company !== undefined)
        updatePayload.company = adminData.company;
      if (adminData.role !== undefined) updatePayload.role = adminData.role;
      if (adminData.status !== undefined)
        updatePayload.status = adminData.status;
      if (adminData.comment !== undefined)
        updatePayload.comment = adminData.comment;

      // Admins CANNOT change passwords via this endpoint
      // Check the original 'body' for password fields attempt
      if (body.currentPassword || body.newPassword) {
        throw createError({
          statusCode: 400,
          statusMessage:
            "Admins cannot change user passwords via this endpoint.",
        });
      }
    } else {
      // Should be caught by checkUserPermissions, but as a fallback
      throw createError({
        statusCode: 403,
        statusMessage: "Forbidden: Insufficient permissions.",
      });
    }

    // Ensure there's something to update
    if (Object.keys(updatePayload).length === 0) {
      throw createError({
        statusCode: 400,
        statusMessage: "No update data provided.",
      });
    }

    // Update user in the database
    const updatedUser = await prisma.user.update({
      where: { id: targetUserId },
      data: updatePayload,
      // Select fields to return (same regardless of who updated)
      select: {
        id: true,
        name: true,
        email: true, // Include email for consistency
        company: true,
        role: true,
        status: true,
        comment: true, // Only relevant if admin updated, but safe to include
        updatedAt: true,
      },
    });

    return updatedUser;
  } catch (error: unknown) {
    // Handle Zod validation errors
    if (error instanceof z.ZodError) {
      throw createError({
        statusCode: 400,
        statusMessage: "Validation error",
        data: error.flatten().fieldErrors, // Use flatten for better structure
      });
    }
    // Re-throw H3 errors (like 401, 403, 404)
    if (error instanceof Error && "statusCode" in error) {
      throw error;
    }
    // Log other errors and throw a generic 500
    console.error(`Error updating user ${targetUserId}:`, error);
    throw createError({
      statusCode: 500,
      statusMessage: "An error occurred while updating user profile.",
    });
  }
});
