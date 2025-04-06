import { z } from 'zod';
import { checkAuth, checkUserPermissions } from '~/server/utils/userUtils';
import prisma from '../../utils/prisma';
import { defineEventHandler, readBody, createError } from 'h3';
export default defineEventHandler(async (event) => {
  // Check if user is authenticated
  const currentUser = checkAuth(event);

  const params = event.context.params || {};
  const id = params.id;
  
  // Check permissions
  const { hasAdminAccess } = checkUserPermissions(currentUser, id);

  try {
    const body = await readBody(event);
    
    // Different validation schema for admins and regular users
    let updateData;
    
    if (hasAdminAccess) {
      // Admin can update all fields
      const schema = z.object({
        name: z.string().optional(),
        company: z.string().optional(),
        role: z.enum(['EMPTY', 'USER', 'DEVELOPER', 'ADMIN', 'SECURITY']).optional(),
        status: z.enum(['ACTIVE', 'EMAIL_NOT_CONFIRMED', 'BLOCKED']).optional(),
        rbac: z.string().optional(),
        comment: z.string().optional(),
      });
      
      updateData = schema.parse(body);
    } else {
      // Regular users can only update their own name and company
      const schema = z.object({
        name: z.string().optional(),
        company: z.string().optional(),
      });
      
      updateData = schema.parse(body);
    }
    
    // Update user
    const user = await prisma.user.update({
      where: { id },
      data: updateData,
      select: {
        id: true,
        name: true,
        email: true,
        company: true,
        role: true,
        status: true,
        comment: true,
        updatedAt: true
      }
    });
    
    return user;
  } catch (error) {
    if (error instanceof z.ZodError) {
      throw createError({
        statusCode: 400,
        statusMessage: 'Validation error',
        data: error.errors
      });
    }
    
    console.error('Error updating user:', error);
    throw createError({
      statusCode: 500,
      statusMessage: 'An error occurred while updating user'
    });
  }
}); 