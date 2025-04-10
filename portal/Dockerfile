# Stage 1: Builder (Installs ALL dependencies, builds the app, generates Prisma client)
FROM node:lts-alpine AS builder
# Install build tools for potential native dependencies (e.g., bcrypt)
RUN apk add --no-cache python3 make g++
WORKDIR /app
COPY package.json package-lock.json* ./
COPY prisma ./prisma/
# Install ALL dependencies (including dev dependencies like Prisma CLI)
RUN npm ci
COPY . .
# Ensure nuxt prepare ran if not covered by `npm ci` postinstall fully
# RUN npx nuxt prepare # Re-running might be redundant but safe
ENV NODE_ENV=production
# Build the Nuxt application
RUN npm run build

# Stage 2: DB Initializer Image (Contains ALL dependencies + Prisma CLI)
# This image is specifically designed to run Prisma commands (migrate, seed)
FROM node:lts-alpine AS db-initializer
# Install build tools for potential native dependencies (e.g., bcrypt)
RUN apk add --no-cache python3 make g++
WORKDIR /app
COPY package.json package-lock.json* ./
# Copy the Prisma schema file BEFORE installing dependencies that might trigger postinstall
COPY --from=builder /app/prisma/schema.prisma ./prisma/schema.prisma
# Install ALL dependencies (including dev, e.g., Prisma CLI)
# The postinstall script (`nuxt prepare`/`prisma generate`) runs here and needs the schema
RUN npm ci
# Copy the rest of the application code, including the seed script and generated client
# This ensures tsx can find the seed script and the generated client is available if needed
COPY --from=builder /app /app

# Stage 3: Runtime Image (Contains the built app + ONLY production dependencies)
# This is the default final stage
FROM node:lts-alpine AS runtime
# Install only necessary runtime OS packages
# bcrypt is a production dependency that might require build tools
RUN apk add --no-cache python3 make g++
WORKDIR /app
ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000
COPY package.json package-lock.json* ./
# Copy the Prisma schema file BEFORE installing dependencies
COPY --from=builder /app/prisma/schema.prisma ./prisma/schema.prisma
# Install ONLY production dependencies AND SKIP postinstall scripts
# The `prisma generate` or `nuxt prepare` in postinstall is not needed here and caused the error
RUN npm ci --only=production --ignore-scripts
# Copy the built Nuxt app from the builder stage
COPY --from=builder /app/.output ./.output
# Copy the generated Prisma client needed at runtime (must be copied AFTER node_modules is created by npm ci)
COPY --from=builder /app/node_modules/.prisma ./node_modules/.prisma

EXPOSE 3000
CMD ["node", ".output/server/index.mjs"]