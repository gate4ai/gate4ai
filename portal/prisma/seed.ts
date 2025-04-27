// gate4ai/portal/prisma/seed.ts
import { PrismaClient, Role, Status } from "@prisma/client";
import bcrypt from "bcrypt";
import crypto from "crypto";

const prisma = new PrismaClient();

async function main() {
  const adminEmail = "admin@gate4.ai";
  const adminPassword = "Admin123!"; // Ensure this matches test expectations if needed
  const adminUser = await prisma.user.findUnique({
    where: { email: adminEmail },
  });

  if (adminUser) {
    await prisma.user.update({
      where: { email: adminEmail },
      data: { role: Role.ADMIN, status: Status.ACTIVE }, // Ensure admin is active
    });
    console.log(`Updated ${adminEmail} to Admin role and Active status`);
  } else {
    const hashedPassword = await bcrypt.hash(adminPassword, 10);
    await prisma.user.create({
      data: {
        id: crypto.randomUUID(), // Explicitly generate UUID
        email: adminEmail,
        password: hashedPassword,
        name: "Admin User",
        role: Role.ADMIN,
        status: Status.ACTIVE, // Create as active
      },
    });
    console.log(`Created admin user ${adminEmail}`);
  }

  // --- Upsert Settings ---
  const settingRecords = [
    {
      key: "general_notification_dynamic", // Keep the dynamic setting
      group: "general",
      name: "Dynamic Global Notification",
      description:
        "Message displayed globally to all users (e.g., maintenance notice). Leave empty to disable.",
      value: "", // Default to empty
      frontend: true, // <<< Needs to be true
    },
    {
      key: "show_owner_email",
      group: "general",
      name: "Show Server Owner Email",
      description:
        "If enabled, server owner email addresses will be visible to users on server info pages",
      value: false,
      frontend: true,
    },
    {
      key: "server_owner_can_see_user_email",
      group: "security",
      name: "Allow Server Owners to See Subscriber Emails",
      description:
        "If enabled, server owners can see the email addresses of their subscribers on the subscription management page.",
      value: true, // Example: Default to true
      frontend: true, // This setting is used by the frontend logic
    },
    {
      key: "email_do_not_send_email",
      group: "email",
      name: "Don't send email",
      description:
        "If true, disables sending all emails (confirmation, password reset). Users are activated immediately.",
      value: true,
      frontend: true,
    },
    {
      key: "email_smtp_server",
      group: "email",
      name: "SMTP Server Configuration",
      description:
        "Configuration for the outgoing email server (see Nodemailer docs: https://nodemailer.com/smtp/)",
      value: {
        host: "smtp.example.com",
        port: 587,
        secure: false,
        auth: {
          user: "your-email@example.com",
          pass: "your-password",
        },
      },
      frontend: false,
    },
    {
      key: "url_how_users_connect_to_the_portal",
      group: "general",
      name: "Portal Base URL",
      description:
        "The base URL where the portal is accessible by users (used for generating links in emails). Example: https://gate4.ai",
      value:
        process.env.URL_HOW_USERS_CONNECT_TO_THE_PORTAL ?? "http://gate4.ai",
      frontend: false,
    },
    {
      key: "only_developer_can_post_server",
      group: "general",
      name: "Only developer can post server",
      description:
        "If the flag is set, only users with the Developer role can publish new servers.",
      value: false,
      frontend: true,
    },
    {
      key: "gateway_log_level",
      group: "gateway",
      name: "Log Level",
      description:
        "Log level for the gateway server. Available options: debug, info, warn, error, dpanic, panic, fatal",
      value: "info",
      frontend: false,
    },
    {
      key: "gateway_listen_address",
      group: "gateway",
      name: "Listen Address",
      description:
        "Address and port the gateway server listens on (format: host:port)",
      value: ":8080",
      frontend: false,
    },
    {
      key: "gateway_server_name",
      group: "gateway",
      name: "Server Name",
      description: "Name of the gateway server for identification purposes",
      value: "Gate4ai Gateway",
      frontend: false,
    },
    {
      key: "gateway_server_version",
      group: "gateway",
      name: "Server Version",
      description: "Version of the gateway server",
      value: "1.0.0",
      frontend: false,
    },
    {
      key: "gateway_authorization_type",
      group: "gateway",
      name: "Authorization Type",
      description:
        "Authorization type for the gateway server (0: AuthorizedUsersOnly, 1: NotAuthorizedToMarkedMethods, 2: NotAuthorizedEverywhere)",
      value: 0,
      frontend: false,
    },
    {
      key: "gateway_reload_every_seconds",
      group: "gateway",
      name: "Reload Interval",
      description:
        "Interval in seconds for reloading configuration from the database",
      value: 60,
      frontend: false,
    },
    {
      key: "url_how_gateway_proxy_connect_to_the_portal",
      group: "gateway",
      name: "Frontend Address for Proxy",
      description:
        "Address where the frontend UI is hosted, used by the proxy handler",
      value:
        process.env.URL_HOW_GATEWAY_PROXY_CONNECT_TO_THE_PORTAL ??
        "http://portal:3000",
      frontend: false,
    },
    {
      key: "general_gateway_address",
      group: "general",
      name: "Gateway address for user",
      description:
        "Address where the user can access the gateway. Empty means that the same address as the frontend is used.",
      value: "",
      frontend: true,
    },
    {
      key: "path_for_discovering_handler",
      group: "general",
      name: "Gateway address of info_handler",
      description: "/discovering  . If clear, then method disabled",
      value: "/discovering",
      frontend: true,
    },
    {
      key: "gateway_ssl_enabled",
      group: "gateway",
      name: "Enable SSL/TLS for Gateway",
      description: "Enable HTTPS for the gateway's main listener.",
      value: false,
      frontend: false,
    },
    {
      key: "gateway_ssl_mode",
      group: "gateway",
      name: "Gateway SSL Mode",
      description:
        "Mode for gateway SSL certificate management ('manual' or 'acme').",
      value: "manual",
      frontend: false,
    },
    {
      key: "gateway_ssl_cert_file",
      group: "gateway",
      name: "Gateway SSL Certificate File",
      description: "Path to the gateway SSL certificate file (manual mode).",
      value: "",
      frontend: false,
    },
    {
      key: "gateway_ssl_key_file",
      group: "gateway",
      name: "Gateway SSL Private Key File",
      description: "Path to the gateway SSL private key file (manual mode).",
      value: "",
      frontend: false,
    },
    {
      key: "gateway_ssl_acme_domains",
      group: "gateway",
      name: "Gateway ACME Domains",
      description:
        "List of domain names for gateway ACME certificate requests (JSON array).",
      value: [],
      frontend: false,
    },
    {
      key: "gateway_ssl_acme_email",
      group: "gateway",
      name: "Gateway ACME Contact Email",
      description:
        "Contact email address for Let's Encrypt notifications for the gateway.",
      value: "",
      frontend: false,
    },
    {
      key: "gateway_ssl_acme_cache_dir",
      group: "gateway",
      name: "Gateway ACME Cache Directory",
      description:
        "Directory to store ACME certificates and state for the gateway.",
      value: "./.autocert-cache",
      frontend: false,
    },
  ];

  for (const record of settingRecords) {
    await prisma.settings.upsert({
      where: { key: record.key },
      update: {
        group: record.group,
        name: record.name,
        description: record.description,
        value: record.value,
        frontend: record.frontend === true,
      },
      create: {
        id: crypto.randomUUID(),
        ...record,
        value: record.value,
        frontend: record.frontend === true,
      },
    });
    console.log(`Upserted setting: ${record.key}`);
  }
}

main()
  .catch((e) => {
    console.error(e);
    process.exit(1);
  })
  .finally(async () => {
    await prisma.$disconnect();
  });
