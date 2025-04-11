import { PrismaClient, Status } from '@prisma/client';
import bcrypt from 'bcrypt';
import jwt from 'jsonwebtoken';
import { z } from 'zod';
import { defineEventHandler, readBody, createError } from 'h3';
import { sendEmail } from '../../utils/email'; // Import the email utility
import crypto from 'crypto'; // Import crypto for token generation

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
    
    // Check Email Setting
    const emailSetting = await prisma.settings.findUnique({
        where: { key: 'email_do_not_send_email' },
        select: { value: true }
    });
    const portalBaseUrlSetting = await prisma.settings.findUnique({
        where: { key: 'url_how_users_connect_to_the_portal'},
        select: { value: true }
    });

    // Default to true (disabled) if setting not found or not boolean
    const doNotSendEmail = !(emailSetting?.value === false);
    // Default base URL if setting not found or not a string
    const portalBaseUrl = typeof portalBaseUrlSetting?.value === 'string' ? portalBaseUrlSetting.value : 'http://localhost:8080'; // Fallback URL

    let userStatus = Status.ACTIVE; // Default to ACTIVE if email is disabled
    let confirmationCode: string | null = null;
    let confirmationExpires: Date | null = null;

    if (!doNotSendEmail) {
      // Email sending is ENABLED
      userStatus = Status.EMAIL_NOT_CONFIRMED;
      confirmationCode = crypto.randomBytes(32).toString('hex');
      confirmationExpires = new Date(Date.now() + 24 * 60 * 60 * 1000); // 24 hours expiry

      // Prepare email content
      const confirmationLink = `${portalBaseUrl}/confirm-email/${confirmationCode}`;
      const subject = 'Confirm your gate4.ai email address';
      const htmlBody = `
        <h1>Welcome to gate4.ai!</h1>
        <p>Please click the link below to confirm your email address:</p>
        <p><a href="${confirmationLink}">${confirmationLink}</a></p>
        <p>This link will expire in 24 hours.</p>
        <p>If you didn't register for this account, please ignore this email.</p>
      `;

      // Send the email (will throw if SMTP fails)
      await sendEmail(email, subject, htmlBody);
    } else {
       console.log(`Email sending disabled. User ${email} will be registered as ACTIVE.`);
    }

    // Hash password
    const hashedPassword = await bcrypt.hash(password, 10);
    
    // Create user
    const user = await prisma.user.create({
      data: {
        name,
        email,
        password: hashedPassword,
        status: userStatus, // Use determined status
        emailConfirmationCode: confirmationCode, // Store code if generated
        emailConfirmationExpires: confirmationExpires, // Store expiry if generated
      },
      // Select necessary fields for response
       select: {
          id: true,
          email: true,
          name: true,
          role: true,
          status: true,
          company: true,
        }
    });
    
    // Generate JWT token (only if user is immediately active)
    let token: string | null = null;
    if (user.status === Status.ACTIVE) {
         const config = useRuntimeConfig();
         token = jwt.sign({ userId: user.id }, config.jwtSecret, { expiresIn: '7d' });
    }

    // Return different responses based on whether confirmation is needed
    if (user.status === Status.EMAIL_NOT_CONFIRMED) {
         return {
            message: 'Registration successful. Please check your email to confirm your account.',
            user: { // Return limited user info
                id: user.id,
                email: user.email,
                name: user.name,
            }
         };
    } else {
        // User is active immediately (email disabled)
        return {
            token, // Include token for immediate login
            user: user // Return full user object (excluding sensitive fields)
        };
    }
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
    
    console.error('Registration API error:', error); // Log the actual error
    throw createError({
      statusCode: 500,
      statusMessage: 'An error occurred during registration'
    });
  } finally {
      await prisma.$disconnect(); // Ensure prisma disconnects
  }
}); 