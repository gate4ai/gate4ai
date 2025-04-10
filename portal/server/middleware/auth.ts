import jwt from 'jsonwebtoken';
import prisma from '../utils/prisma';
import { defineEventHandler, getRequestURL, getRequestHeader } from 'h3';

export default defineEventHandler(async (event) => {
  // Skip auth for non-API routes or public API routes
  const path = getRequestURL(event).pathname;
  if (!path.startsWith('/api/') || 
      path.startsWith('/api/auth/login') || 
      path.startsWith('/api/auth/register')) {
    return;
  }
  
  // Get token from headers
  const authHeader = getRequestHeader(event, 'authorization');
  if (!authHeader || !authHeader.startsWith('Bearer ')) {
    return; // No token, continue as unauthenticated
  }
  
  const token = authHeader.substring(7); // Remove 'Bearer ' prefix
  
  try {
    // Verify token
    const config = useRuntimeConfig();
    const decoded = jwt.verify(token, config.portalJwtSecret) as { userId: string };
    
    // Get user from database
    const user = await prisma.user.findUnique({
      where: { id: decoded.userId },
      select: {
        id: true,
        email: true,
        name: true,
        role: true,
        status: true,
        company: true,
      }
    });
    
    if (!user) {
      return; // User not found, continue as unauthenticated
    }
    
    // Set user in context
    event.context.user = user;
  } catch (error) {
    // Invalid token, continue as unauthenticated
    console.error('Auth error:', error);
  }
});