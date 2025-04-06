import type { User } from '@prisma/client';
import type { H3Event } from 'h3';
import { createError } from 'h3';

// Check if a user is a security or admin user
export const isSecurityOrAdminUser = (user: User) => {
  return user?.role === 'ADMIN' || user?.role === 'SECURITY';
};

// Check if a user is authenticated - Moved from api-helpers.ts
export function checkAuth(event: H3Event) {
  const user = event.context.user;
  if (!user) {
    throw createError({
      statusCode: 401,
      statusMessage: 'Unauthorized'
    });
  }
  return user;
}

// Check if user has permission to access/modify user data
export const checkUserPermissions = (currentUser: User, targetUserId: string) => {
  const isSelfUpdate = currentUser.id === targetUserId;
  const hasAdminAccess = isSecurityOrAdminUser(currentUser);
  
  if (!hasAdminAccess && !isSelfUpdate) {
    throw createError({
      statusCode: 403,
      statusMessage: 'Forbidden'
    });
  }
  
  return { isSelfUpdate, hasAdminAccess };
};

// Get user select fields based on permissions
export const getUserSelectFields = (hasAdminAccess: boolean) => {
  if (hasAdminAccess) {
    return {
      id: true,
      name: true,
      email: true,
      company: true,
      role: true,
      status: true,
      comment: true,
      createdAt: true,
      updatedAt: true
    };
  }
  
  return {
    id: true,
    name: true,
    email: true,
    company: true,
    status: true,
    createdAt: true,
    updatedAt: true
  };
}; 