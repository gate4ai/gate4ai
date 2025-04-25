import { PrismaClient, Status } from '@prisma/client';
import { defineEventHandler, getRouterParam, createError, sendRedirect } from 'h3';

const prisma = new PrismaClient();

export default defineEventHandler(async (event) => {
  const code = getRouterParam(event, 'code');

  if (!code) {
    // Redirect to an error page or login page if code is missing
    // console.error("Confirmation attempt with missing code.");
    // await sendRedirect(event, '/login?error=invalid_confirmation', 302);
    // return;
     throw createError({ statusCode: 400, statusMessage: 'Confirmation code is missing.' });
  }

  try {
    const user = await prisma.user.findFirst({
      where: {
        emailConfirmationCode: code,
        // Optional: Check expiry
        // emailConfirmationExpires: {
        //   gt: new Date() // Ensure code hasn't expired
        // }
      },
    });

    if (!user) {
      // Code not found or expired
      // Redirect to an error/info page on the frontend
       console.warn(`Invalid or expired confirmation code used: ${code}`);
      // await sendRedirect(event, '/login?error=invalid_confirmation', 302); // Example redirect
      // return;
       throw createError({ statusCode: 400, statusMessage: 'Invalid or expired confirmation code.' });

    }

    // Update user status to ACTIVE and clear confirmation fields
    await prisma.user.update({
      where: { id: user.id },
      data: {
        status: Status.ACTIVE,
        emailConfirmationCode: null, // Clear the code
        emailConfirmationExpires: null,
      },
    });

    console.log(`Email confirmed successfully for user: ${user.email}`);

    // Redirect user to the login page with a success message
    await sendRedirect(event, '/login?confirmed=true', 302); // 302 Found redirect
    return; // End handler execution

  } catch (error: unknown) {
    console.error(`Error during email confirmation for code ${code}:`, error);
     throw createError({ statusCode: 500, statusMessage: 'An error occurred during email confirmation.' });
  } finally {
     await prisma.$disconnect();
  }
}); 