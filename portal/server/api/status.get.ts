import prisma from '../utils/prisma';
import { defineEventHandler, createError } from 'h3';

export default defineEventHandler(async () => {
  try {
    // Check database connection by running a simple query
    await prisma.$queryRaw`SELECT 1`;
    
    // If query succeeds, return 200 with success message
    return { database: "ok" };
  } catch (error) {
    // If query fails, return 500 with error message
    throw createError({
      statusCode: 500,
      data: { database: "error" }
    });
  }
}); 