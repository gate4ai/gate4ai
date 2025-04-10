import { PrismaClient } from '@prisma/client';
import { z } from 'zod';
import { defineEventHandler, readBody, createError } from 'h3';
import { sendEmail } from '../../utils/email';
import crypto from 'crypto';

const prisma = new PrismaClient();

const forgotPasswordSchema = z.object({
  email: z.string().email("Invalid email format"),
}).strict();

export default defineEventHandler(async (event) => {
  try {
    // Check email setting first
    const emailSetting = await prisma.settings.findUnique({
        where: { key: 'email_do_not_send_email' },
        select: { value: true }
    });
    const doNotSendEmail = !(emailSetting?.value === false);

    if (doNotSendEmail) {
        console.log("Forgot password attempt while email is disabled.");
        // Return a generic message even if disabled, avoid confirming email existence
        //return { message: "If an account with that email exists, instructions may be sent." };
        // Or throw an error:
        throw createError({ statusCode: 400, statusMessage: 'Password reset via email is disabled.' });
    }

    // Validate body
    const body = await readBody(event);
    const validationResult = forgotPasswordSchema.safeParse(body);
    if (!validationResult.success) {
      throw createError({ statusCode: 400, statusMessage: 'Validation Error', data: validationResult.error.flatten().fieldErrors });
    }
    const { email } = validationResult.data;

    // Find user by email
    const user = await prisma.user.findUnique({
      where: { email: email.toLowerCase() }, // Use lowercase for lookup consistency
    });

    // IMPORTANT: Always return a generic success message even if the user doesn't exist
    // This prevents email enumeration attacks. The email sending logic below will simply not run.
    if (!user) {
      console.log(`Password reset requested for non-existent email: ${email}`);
       return { message: "If an account with that email exists, password reset instructions have been sent." };
    }

    // Generate reset token and expiry
    const resetCode = crypto.randomBytes(32).toString('hex');
    const resetExpires = new Date(Date.now() + 1 * 60 * 60 * 1000); // 1 hour expiry

    // Store the reset token and expiry in the user record
    await prisma.user.update({
      where: { id: user.id },
      data: {
        resetPasswordCode: resetCode,
        resetPasswordExpires: resetExpires,
      },
    });

    // Get Portal Base URL for the link
    const portalBaseUrlSetting = await prisma.settings.findUnique({
        where: { key: 'url_how_users_connect_to_the_portal'},
        select: { value: true }
    });
     const portalBaseUrl = typeof portalBaseUrlSetting?.value === 'string' ? portalBaseUrlSetting.value : 'http://localhost:8080'; // Fallback

    // Send the password reset email
    const resetLink = `${portalBaseUrl}/reset-password/${resetCode}`;
    const subject = 'Reset your gate4.ai password';
    const htmlBody = `
      <h1>Reset Your Password</h1>
      <p>You requested a password reset for your gate4.ai account (${user.email}).</p>
      <p>Click the link below to set a new password:</p>
      <p><a href="${resetLink}">${resetLink}</a></p>
      <p>This link is valid for 1 hour.</p>
      <p>If you didn't request this, please ignore this email.</p>
    `;

    await sendEmail(user.email, subject, htmlBody);

     return { message: "Password reset instructions have been sent to your email address." };

  } catch (error: any) {
    console.error('Forgot password API error:', error);
    if (error instanceof z.ZodError || (error.statusCode && error.statusCode !== 500)) { // Don't mask validation/auth errors
        throw error;
    }
    // For other errors, return a generic message to avoid leaking info
    return { message: "An error occurred. Please try again later." };
  } finally {
     await prisma.$disconnect();
  }
}); 