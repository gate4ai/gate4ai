import { checkAuth, checkUserPermissions, getUserSelectFields } from '~/server/utils/userUtils';
import prisma from '../../utils/prisma';
import { defineEventHandler, createError } from 'h3';

export default defineEventHandler(async (event) => {
  // Check if user is authenticated
  const currentUser = checkAuth(event);

  const params = event.context.params || {};
  const id = params.id;
  
  // Check permissions
  const { hasAdminAccess } = checkUserPermissions(currentUser, id);

  try {
    // Get select fields based on permissions
    const select = getUserSelectFields(hasAdminAccess);
    
    const user = await prisma.user.findUnique({
      where: { id },
      select
    });
  
    if (!user) {
      throw createError({
        statusCode: 404,
        statusMessage: 'User not found'
      });
    }
    
    return user;
  } catch (error) {
    console.error('Error fetching user:', error);
    throw createError({
      statusCode: 500,
      statusMessage: 'An error occurred while fetching user'
    });
  }
}); 