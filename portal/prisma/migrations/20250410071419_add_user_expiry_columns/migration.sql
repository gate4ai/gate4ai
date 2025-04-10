/*
  Warnings:

  - A unique constraint covering the columns `[emailConfirmationCode]` on the table `User` will be added. If there are existing duplicate values, this will fail.
  - A unique constraint covering the columns `[resetPasswordCode]` on the table `User` will be added. If there are existing duplicate values, this will fail.

*/
-- AlterTable
ALTER TABLE "User" ADD COLUMN     "emailConfirmationExpires" TIMESTAMP(3),
ADD COLUMN     "resetPasswordExpires" TIMESTAMP(3);

-- CreateIndex
CREATE UNIQUE INDEX "User_emailConfirmationCode_key" ON "User"("emailConfirmationCode");

-- CreateIndex
CREATE UNIQUE INDEX "User_resetPasswordCode_key" ON "User"("resetPasswordCode");
