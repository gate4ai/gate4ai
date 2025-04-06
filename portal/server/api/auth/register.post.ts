import { PrismaClient } from '@prisma/client';
import bcrypt from 'bcrypt';
import jwt from 'jsonwebtoken';
import { z } from 'zod';
import { defineEventHandler, readBody, createError } from 'h3';

const prisma = new PrismaClient();

export default defineEventHandler(async (event) => {
  try {
    // Validate request body
    const schema = z.object({
      name: z.string().min(1),
      email: z.string().email(),
      password: z.string().min(8)
    });
    
    const body = await readBody(event);
    const { name, email, password } = schema.parse(body);
    
    // Check if user already exists
    const existingUser = await prisma.user.findUnique({
      where: { email }
    });
    
    if (existingUser) {
      throw createError({
        statusCode: 400,
        statusMessage: 'Email already in use'
      });
    }
    
    // Hash password
    const hashedPassword = await bcrypt.hash(password, 10);
    
    // Create user
    const user = await prisma.user.create({
      data: {
        name,
        email,
        password: hashedPassword,
        status: 'EMAIL_NOT_CONFIRMED'
      }
    });
    
    // Generate JWT token
    const config = useRuntimeConfig();
    const token = jwt.sign(
      { userId: user.id },
      config.jwtSecret,
      { expiresIn: '7d' }
    );
    
    return {
      token,
      user: {
        id: user.id,
        email: user.email,
        name: user.name,
        role: user.role,
        status: user.status,
        company: user.company
      }
    };
  } catch (
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    error: any
  ) {
    if (error instanceof z.ZodError) {
      throw createError({
        statusCode: 400,
        statusMessage: 'Validation error',
        data: error.errors
      });
    }
    
    // Re-throw existing errors
    if (error.statusCode) {
      throw error;
    }
    
    console.error('Registration error:', error);
    throw createError({
      statusCode: 500,
      statusMessage: 'An error occurred during registration'
    });
  }
}); 