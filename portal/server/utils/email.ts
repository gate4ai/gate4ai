import nodemailer from "nodemailer";
import prisma from "./prisma"; // Import your prisma client utility
import { createError } from "h3";

interface SmtpConfigAuth {
  user: string;
  pass: string;
}

interface SmtpConfig {
  host: string;
  port: number;
  secure: boolean;
  auth?: SmtpConfigAuth; // Make auth optional
}

let smtpSettingsCache: SmtpConfig | null = null;
let doNotSendEmailCache: boolean | null = null;
let lastSettingsCheck: Date | null = null;
const CACHE_DURATION = 5 * 60 * 1000; // 5 minutes in milliseconds

async function getEmailSettings(): Promise<{
  smtpConfig: SmtpConfig | null;
  doNotSend: boolean;
}> {
  const now = new Date();
  // Check cache validity
  if (
    lastSettingsCheck !== null &&
    now.getTime() - lastSettingsCheck.getTime() < CACHE_DURATION
  ) {
    if (doNotSendEmailCache !== null && smtpSettingsCache !== null) {
      // console.log("Using cached email settings");
      return { smtpConfig: smtpSettingsCache, doNotSend: doNotSendEmailCache };
    }
  }
  // console.log("Fetching fresh email settings from DB");

  try {
    const settings = await prisma.settings.findMany({
      where: {
        key: {
          in: ["email_do_not_send_email", "email_smtp_server"],
        },
      },
      select: { key: true, value: true },
    });

    let fetchedSmtpConfig: SmtpConfig | null = null;
    let fetchedDoNotSend: boolean = true; // Default to true (disabled) if setting not found

    settings.forEach((setting) => {
      if (
        setting.key === "email_do_not_send_email" &&
        typeof setting.value === "boolean"
      ) {
        fetchedDoNotSend = setting.value;
      } else if (setting.key === "email_smtp_server") {
        // Basic type check for the SMTP config object
        const val = setting.value as Record<string, unknown>;
        if (
          typeof val === "object" &&
          val !== null &&
          typeof val.host === "string" &&
          typeof val.port === "number"
        ) {
          fetchedSmtpConfig = {
            host: val.host,
            port: val.port,
            secure: typeof val.secure === "boolean" ? val.secure : false, // Default secure to false
            // Check for auth object existence and types
            auth:
              typeof val.auth === "object" &&
              val.auth !== null &&
              val.auth &&
              typeof (val.auth as Record<string, unknown>).user === "string" &&
              typeof (val.auth as Record<string, unknown>).pass === "string"
                ? {
                    user: (val.auth as Record<string, string>).user,
                    pass: (val.auth as Record<string, string>).pass,
                  }
                : undefined, // Set auth to undefined if invalid or missing
          };
        } else {
          console.error(
            "Invalid format for email_smtp_server setting:",
            setting.value
          );
        }
      }
    });

    // Update cache
    smtpSettingsCache = fetchedSmtpConfig;
    doNotSendEmailCache = fetchedDoNotSend;
    lastSettingsCheck = now;

    return { smtpConfig: fetchedSmtpConfig, doNotSend: fetchedDoNotSend };
  } catch (error) {
    console.error("Failed to fetch email settings from database:", error);
    // Return default disabled state on error
    return { smtpConfig: null, doNotSend: true };
  }
}

export async function sendEmail(
  to: string,
  subject: string,
  htmlBody: string
): Promise<void> {
  const { smtpConfig, doNotSend } = await getEmailSettings();

  if (doNotSend) {
    console.log(
      `Email sending skipped (disabled by setting) for subject: ${subject}`
    );
    return; // Don't send if disabled
  }

  if (!smtpConfig || !smtpConfig.host) {
    console.error(
      "SMTP server configuration is missing or invalid. Cannot send email."
    );
    throw createError({
      statusCode: 500,
      statusMessage: "Email configuration error",
    });
  }

  // Create a transporter object using the default SMTP transport
  const transporterOptions: nodemailer.TransportOptions = {
    host: smtpConfig.host,
    port: smtpConfig.port,
    secure: smtpConfig.secure, // true for 465, false for other ports
    // Add ignoreTLS option if needed for local testing without valid certs
    // tls: {
    //     rejectUnauthorized: false // Use cautiously only for local/testing
    // }
  };

  // Add auth only if it's defined and valid
  if (smtpConfig.auth && smtpConfig.auth.user && smtpConfig.auth.pass) {
    transporterOptions.auth = {
      user: smtpConfig.auth.user,
      pass: smtpConfig.auth.pass,
    };
  } else {
    // Log structure of auth object if incomplete
    console.warn(
      "SMTP Auth details missing or incomplete, attempting unauthenticated connection.",
      { authConfig: smtpConfig.auth }
    );
  }

  const transporter = nodemailer.createTransport(transporterOptions);

  // Send mail with defined transport object
  const mailOptions = {
    from: `"gate4.ai" <${smtpConfig.auth?.user || "noreply@gate4.ai"}>`, // Sender address (use config user or default)
    to: to, // List of receivers
    subject: subject, // Subject line
    html: htmlBody, // HTML body content
  };

  try {
    const info = await transporter.sendMail(mailOptions);
    console.log(`Message sent: ${info.messageId} to ${to}`);
  } catch (error) {
    console.error(`Error sending email to ${to}:`, error);
    // Throw a specific error that can be caught by API handlers
    throw createError({
      statusCode: 500,
      statusMessage: `Failed to send email: ${
        error instanceof Error ? error.message : "Unknown SMTP error"
      }`,
    });
  }
}
