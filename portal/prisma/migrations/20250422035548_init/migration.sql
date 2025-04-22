-- CreateEnum
CREATE TYPE "Role" AS ENUM ('USER', 'DEVELOPER', 'ADMIN', 'SECURITY');

-- CreateEnum
CREATE TYPE "Status" AS ENUM ('EMAIL_NOT_CONFIRMED', 'ACTIVE', 'BLOCKED');

-- CreateEnum
CREATE TYPE "ServerProtocol" AS ENUM ('MCP', 'A2A', 'REST');

-- CreateEnum
CREATE TYPE "ServerStatus" AS ENUM ('DRAFT', 'ACTIVE', 'BLOCKED');

-- CreateEnum
CREATE TYPE "ServerAvailability" AS ENUM ('PUBLIC', 'PRIVATE', 'SUBSCRIPTION');

-- CreateEnum
CREATE TYPE "SubscriptionStatus" AS ENUM ('PENDING', 'ACTIVE', 'BLOCKED');

-- CreateTable
CREATE TABLE "User" (
    "id" TEXT NOT NULL,
    "email" TEXT NOT NULL,
    "password" TEXT,
    "name" TEXT,
    "company" TEXT,
    "emailConfirmationCode" TEXT,
    "emailConfirmationExpires" TIMESTAMP(3),
    "resetPasswordCode" TEXT,
    "resetPasswordExpires" TIMESTAMP(3),
    "status" "Status" NOT NULL DEFAULT 'EMAIL_NOT_CONFIRMED',
    "comment" TEXT,
    "role" "Role" NOT NULL DEFAULT 'USER',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(3) NOT NULL,

    CONSTRAINT "User_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ServerOwner" (
    "serverId" TEXT NOT NULL,
    "userId" TEXT NOT NULL,

    CONSTRAINT "ServerOwner_pkey" PRIMARY KEY ("serverId","userId")
);

-- CreateTable
CREATE TABLE "ApiKey" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "keyHash" TEXT NOT NULL,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(3) NOT NULL,
    "lastUsed" TIMESTAMP(3),
    "userId" TEXT NOT NULL,

    CONSTRAINT "ApiKey_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "Server" (
    "id" TEXT NOT NULL,
    "slug" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "description" TEXT,
    "website" TEXT,
    "email" TEXT,
    "imageUrl" TEXT,
    "protocol" "ServerProtocol" NOT NULL DEFAULT 'MCP',
    "protocolVersion" TEXT,
    "serverUrl" TEXT NOT NULL,
    "status" "ServerStatus" NOT NULL DEFAULT 'DRAFT',
    "availability" "ServerAvailability" NOT NULL DEFAULT 'SUBSCRIPTION',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(3) NOT NULL,

    CONSTRAINT "Server_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "Tool" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "description" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(3) NOT NULL,
    "serverId" TEXT NOT NULL,

    CONSTRAINT "Tool_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ToolParameter" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "type" TEXT NOT NULL,
    "description" TEXT,
    "required" BOOLEAN NOT NULL DEFAULT false,
    "toolId" TEXT NOT NULL,

    CONSTRAINT "ToolParameter_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "Subscription" (
    "id" TEXT NOT NULL,
    "status" "SubscriptionStatus" NOT NULL DEFAULT 'ACTIVE',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "userId" TEXT,
    "serverId" TEXT,

    CONSTRAINT "Subscription_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ToolCall" (
    "id" TEXT NOT NULL,
    "sessionId" TEXT NOT NULL,
    "requestId" TEXT,
    "serverRequestId" TEXT,
    "request" JSONB NOT NULL,
    "response" JSONB,
    "latency" INTEGER,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "serverId" TEXT,
    "userId" TEXT,
    "toolName" TEXT,

    CONSTRAINT "ToolCall_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "Settings" (
    "id" TEXT NOT NULL,
    "key" TEXT NOT NULL,
    "group" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "description" TEXT NOT NULL,
    "value" JSONB NOT NULL,
    "frontend" BOOLEAN NOT NULL DEFAULT false,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(3) NOT NULL,

    CONSTRAINT "Settings_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "A2ASkill" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "description" TEXT,
    "tags" TEXT[],
    "examples" TEXT[],
    "inputModes" TEXT[],
    "outputModes" TEXT[],
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(3) NOT NULL,
    "serverId" TEXT NOT NULL,

    CONSTRAINT "A2ASkill_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "RESTEndpoint" (
    "id" TEXT NOT NULL,
    "path" TEXT NOT NULL,
    "method" TEXT NOT NULL,
    "description" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(3) NOT NULL,
    "serverId" TEXT NOT NULL,

    CONSTRAINT "RESTEndpoint_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "RESTParameter" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "type" TEXT NOT NULL,
    "description" TEXT,
    "required" BOOLEAN NOT NULL DEFAULT false,
    "endpointId" TEXT NOT NULL,

    CONSTRAINT "RESTParameter_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "RESTRequestBody" (
    "id" TEXT NOT NULL,
    "description" TEXT,
    "example" TEXT,
    "endpointId" TEXT NOT NULL,

    CONSTRAINT "RESTRequestBody_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "RESTResponse" (
    "id" TEXT NOT NULL,
    "statusCode" INTEGER NOT NULL,
    "description" TEXT NOT NULL,
    "example" TEXT,
    "endpointId" TEXT NOT NULL,

    CONSTRAINT "RESTResponse_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "User_email_key" ON "User"("email");

-- CreateIndex
CREATE UNIQUE INDEX "User_emailConfirmationCode_key" ON "User"("emailConfirmationCode");

-- CreateIndex
CREATE UNIQUE INDEX "User_resetPasswordCode_key" ON "User"("resetPasswordCode");

-- CreateIndex
CREATE UNIQUE INDEX "ApiKey_keyHash_key" ON "ApiKey"("keyHash");

-- CreateIndex
CREATE UNIQUE INDEX "Server_slug_key" ON "Server"("slug");

-- CreateIndex
CREATE UNIQUE INDEX "Tool_name_serverId_key" ON "Tool"("name", "serverId");

-- CreateIndex
CREATE UNIQUE INDEX "Subscription_userId_serverId_key" ON "Subscription"("userId", "serverId");

-- CreateIndex
CREATE UNIQUE INDEX "Settings_key_key" ON "Settings"("key");

-- CreateIndex
CREATE UNIQUE INDEX "A2ASkill_name_serverId_key" ON "A2ASkill"("name", "serverId");

-- CreateIndex
CREATE UNIQUE INDEX "RESTEndpoint_path_method_serverId_key" ON "RESTEndpoint"("path", "method", "serverId");

-- CreateIndex
CREATE UNIQUE INDEX "RESTRequestBody_endpointId_key" ON "RESTRequestBody"("endpointId");

-- AddForeignKey
ALTER TABLE "ServerOwner" ADD CONSTRAINT "ServerOwner_userId_fkey" FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ServerOwner" ADD CONSTRAINT "ServerOwner_serverId_fkey" FOREIGN KEY ("serverId") REFERENCES "Server"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ApiKey" ADD CONSTRAINT "ApiKey_userId_fkey" FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "Tool" ADD CONSTRAINT "Tool_serverId_fkey" FOREIGN KEY ("serverId") REFERENCES "Server"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ToolParameter" ADD CONSTRAINT "ToolParameter_toolId_fkey" FOREIGN KEY ("toolId") REFERENCES "Tool"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "Subscription" ADD CONSTRAINT "Subscription_userId_fkey" FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "Subscription" ADD CONSTRAINT "Subscription_serverId_fkey" FOREIGN KEY ("serverId") REFERENCES "Server"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ToolCall" ADD CONSTRAINT "ToolCall_serverId_fkey" FOREIGN KEY ("serverId") REFERENCES "Server"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ToolCall" ADD CONSTRAINT "ToolCall_userId_fkey" FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "A2ASkill" ADD CONSTRAINT "A2ASkill_serverId_fkey" FOREIGN KEY ("serverId") REFERENCES "Server"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "RESTEndpoint" ADD CONSTRAINT "RESTEndpoint_serverId_fkey" FOREIGN KEY ("serverId") REFERENCES "Server"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "RESTParameter" ADD CONSTRAINT "RESTParameter_endpointId_fkey" FOREIGN KEY ("endpointId") REFERENCES "RESTEndpoint"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "RESTRequestBody" ADD CONSTRAINT "RESTRequestBody_endpointId_fkey" FOREIGN KEY ("endpointId") REFERENCES "RESTEndpoint"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "RESTResponse" ADD CONSTRAINT "RESTResponse_endpointId_fkey" FOREIGN KEY ("endpointId") REFERENCES "RESTEndpoint"("id") ON DELETE CASCADE ON UPDATE CASCADE;
