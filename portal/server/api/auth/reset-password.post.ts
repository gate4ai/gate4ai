import { PrismaClient } from '@prisma/client';
import bcrypt from 'bcrypt';
import { z } from 'zod';
import { defineEventHandler, readBody, createError } from 'h3';

const prisma = new PrismaClient();

const resetPasswordSchema = z.object({
  token: z.string().min(1, "Reset token is required"),
  newPassword: z.string().min(8, "Password must be at least 8 characters"),
}).strict();

export default defineEventHandler(async (event) => {
  try {
    // Validate body
    const body = await readBody(event);
    const validationResult = resetPasswordSchema.safeParse(body);
     if (!validationResult.success) {
        throw createError({ statusCode: 400, statusMessage: 'Validation Error', data: validationResult.error.flatten().fieldErrors });
    }
    const { token, newPassword } = validationResult.data;

    // Find user by the reset token AND check expiry
    const user = await prisma.user.findFirst({
      where: {
        resetPasswordCode: token,
        resetPasswordExpires: {
          gt: new Date(), // Check if the expiry time is greater than now
        },
      },
    });

    if (!user) {
      throw createError({ statusCode: 400, statusMessage: 'Invalid or expired password reset token.' });
    }

    // Hash the new password
    const hashedNewPassword = await bcrypt.hash(newPassword, 10);

    // Update the user's password and clear reset token fields
    await prisma.user.update({
      where: { id: user.id },
      data: {
        password: hashedNewPassword,
        resetPasswordCode: null, // Invalidate the token
        resetPasswordExpires: null,
      },
    });

    console.log(`Password reset successfully for user: ${user.email}`);
    return { message: 'Password has been reset successfully.' };

  } catch (error: any) {
    console.error('Reset password API error:', error);
     if (error instanceof z.ZodError || (error.statusCode)) { // Re-throw validation and known H3 errors
        throw error;
    }
    throw createError({ statusCode: 500, statusMessage: 'An error occurred while resetting the password.' });
  } finally {
     await prisma.$disconnect();
  }
}); 