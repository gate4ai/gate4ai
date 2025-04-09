import { checkAuth, isSecurityOrAdminUser, getUserSelectFields } from '~/server/utils/userUtils';
import prisma from '../../utils/prisma';
import { defineEventHandler, getQuery, createError } from 'h3';

export default defineEventHandler(async (event) => {
  try {
        // Check if user is authenticated
    const currentUser = checkAuth(event);
    const hasAdminAccess = isSecurityOrAdminUser(currentUser)

    // Only admin or security users can access all users
    if (!hasAdminAccess) {
      throw createError({
        statusCode: 403,
        statusMessage: 'Forbidden'
      });
    }

    
    // Get query parameters
    const query = getQuery(event);
    const search = query.search as string || '';

    const select = getUserSelectFields(hasAdminAccess);

    // Fetch users with filtering
    const users = await prisma.user.findMany({
      where: {
        OR: [
          { name: { contains: search, mode: 'insensitive' } },
          { email: { contains: search, mode: 'insensitive' } },
          { company: { contains: search, mode: 'insensitive' } }
        ]
      },
      select,
      orderBy: {
        createdAt: 'desc'
      }
    });
    
    return users;
  } catch (error) {
    console.error('Error fetching users:', error);
    throw createError({
      statusCode: 500,
      statusMessage: 'An error occurred while fetching users'
    });
  }
}); 