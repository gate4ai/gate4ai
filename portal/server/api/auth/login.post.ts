import { PrismaClient } from '@prisma/client';
import bcrypt from 'bcrypt';
import jwt from 'jsonwebtoken';
import { z } from 'zod';

const prisma = new PrismaClient();

export default defineEventHandler(async (event) => {
  try {
    // Validate request body
    const schema = z.object({
      email: z.string().email(),
      password: z.string().min(1)
    });
    
    const body = await readBody(event);
    const { email, password } = schema.parse(body);
    
    // Find user by email
    const user = await prisma.user.findUnique({
      where: { email }
    });
    
    if (!user || !user.password) {
      throw createError({
        statusCode: 401,
        statusMessage: 'Invalid email or password'
      });
    }    

    // Compare passwords
    const isPasswordValid = await bcrypt.compare(password, user.password);
    if (!isPasswordValid) {
      throw createError({
        statusCode: 401,
        statusMessage: 'Invalid email or password'
      });
    }
    
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
    
    console.error('Login error:', error);
    throw createError({
      statusCode: 500,
      statusMessage: 'An error occurred during login'
    });
  }
}); 