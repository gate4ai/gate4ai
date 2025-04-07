import { PrismaClient, Role, Status } from '@prisma/client';
import bcrypt from 'bcrypt';
import crypto from 'crypto';

// Helper function to hash API keys using SHA-256
function hashApiKey(key: string): string {
  const hash = crypto.createHash('sha256');
  hash.update(key);
  return hash.digest('hex');
}

const prisma = new PrismaClient();

async function main() {
  // Check if user exists
  const user = await prisma.user.findUnique({
    where: { email: 'admin@gate4.ai' }
  });

  let adminId: string;

  if (user) {
    // Update existing user
    await prisma.user.update({
      where: { email: 'admin@gate4.ai' },
      data: {
        role: Role.ADMIN,
        status: Status.ACTIVE,
      }
    });
    console.log('Updated admin@gate4.ai to Admin role');
    adminId = user.id;
  } else {
    // Create new admin user
    const hashedPassword = await bcrypt.hash('Admin123!', 10);
    const newAdmin = await prisma.user.create({
      data: {
        email: 'admin@gate4.ai',
        password: hashedPassword,
        name: 'Admin User',
        role: Role.ADMIN,
        status: Status.ACTIVE,
      }
    });
    console.log('Created admin user admin@gate4.ai');
    adminId = newAdmin.id;
  }

  // Add Settings
  const settingRecords = [
    {
      key: 'email_do_not_send_email',
      group: 'email',
      name: "Don't send email",
      description: "If true, disables sending all emails (confirmation, password reset). Users are activated immediately.",
      value: true,
      frontend: true
    },
    {
      key: 'email_smtp_server',
      group: 'email',
      name: 'SMTP Server Configuration',
      description: 'Configuration for the outgoing email server (see Nodemailer docs: https://nodemailer.com/smtp/)',
      value: {
        host: "smtp.example.com",
        port: 587,
        secure: false, // use true for port 465, false for other ports
        auth: {
          user: "your-email@example.com",
          pass: "your-password",
        },
      },
      frontend: false // Keep SMTP credentials backend-only
    },
    {
      key: 'general_portal_base_url',
      group: 'general',
      name: 'Portal Base URL',
      description: 'The base URL where the portal is accessible by users (used for generating links in emails). Example: https://gate4.ai',
      value: 'http://localhost:8080', // Default to localhost:8080 (Gateway URL)
      frontend: false // Backend only
    },
    {
      key: 'only_developer_can_post_server',
      group: 'general',
      name: 'Only developer can post server',
      description: 'If the flag is set, only users with the Developer role can publish new servers.',
      value: false,
      frontend: true
    },
    {
      key: 'gateway_log_level',
      group: 'gateway',
      name: 'Log Level',
      description: 'Log level for the gateway server. Available options: debug, info, warn, error, dpanic, panic, fatal',
      value: 'info'
    },
    {
      key: 'gateway_listen_address',
      group: 'gateway',
      name: 'Listen Address',
      description: 'Address and port the gateway server listens on (format: host:port)',
      value: ':8080'
    },
    {
      key: 'gateway_server_name',
      group: 'gateway',
      name: 'Server Name',
      description: 'Name of the gateway server for identification purposes',
      value: 'Gate4ai Gateway'
    },
    {
      key: 'gateway_server_version',
      group: 'gateway',
      name: 'Server Version',
      description: 'Version of the gateway server',
      value: '1.0.0'
    },
    {
      key: 'gateway_authorization_type',
      group: 'gateway',
      name: 'Authorization Type',
      description: 'Authorization type for the gateway server (0: AuthorizedUsersOnly, 1: NotAuthorizedToMarkedMethods, 2: NotAuthorizedEverywhere)',
      value: 0
    },
    {
      key: 'gateway_reload_every_seconds',
      group: 'gateway',
      name: 'Reload Interval',
      description: 'Interval in seconds for reloading configuration from the database',
      value: 60
    },
    {
      key: 'gateway_frontend_address_for_proxy',
      group: 'gateway',
      name: 'Frontend Address for Proxy',
      description: 'Address where the frontend UI is hosted, used by the proxy handler',
      value: 'http://localhost:3000'
    },
    {
      key: 'general_gateway_address',
      group: 'general',
      name: 'Gateway address for user',
      description: 'Address where the user can access the gateway. Empty means that the same address as the frontend is used.',
      value: '',
      frontend: true
    },
    {
      key: 'general_gateway_address_for_backend',
      group: 'general',
      name: 'Gateway address for portal\'s backend',
      description: 'Address where the portal\'s backend can access the gateway',
      value: 'https://gate4.ai'
    },
    {
      key: 'general_gateway_info_handler',
      group: 'general',
      name: 'Gateway address of info_handler',
      description: '/info  . If clear, then method disabled',
      value: '/info',
      frontend: true
    }
  ];

  for (const record of settingRecords) {
    const existingSetting = await prisma.settings.findUnique({
      where: { key: record.key }
    });

    if (existingSetting) {
      // Update existing setting
      await prisma.settings.update({
        where: { key: record.key },
        data: {
          group: record.group,
          name: record.name,
          description: record.description,
          value: record.value
        }
      });
      console.log(`Updated setting: ${record.key}`);
    } else {
      // Create new setting
      await prisma.settings.create({
        data: record
      });
      console.log(`Created setting: ${record.key}`);
    }
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