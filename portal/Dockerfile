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
# Run nuxt prepare which includes prisma generate
RUN npx nuxt prepare
ENV NODE_ENV=production
# Build the Nuxt application
RUN npm run build

# Stage 2: DB Initializer Image (Contains ALL dependencies + Prisma CLI)
# This image is specifically designed to run Prisma commands (migrate, seed)
FROM node:lts-alpine AS db-initializer
# bcrypt is a production dependency and might require build tools during seeding
RUN apk add --no-cache python3 make g++
WORKDIR /app
COPY package.json package-lock.json* ./
# Copy Prisma schema + generated client from the builder stage
COPY --from=builder /app/prisma ./prisma
# Install ALL dependencies (including Prisma CLI)
RUN npm ci
# Copy the rest of the application code, including the seed script (if run via tsx/node)
# If seeding is done via `npx prisma db seed`, `npm ci` already installed everything needed
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
# Install ONLY production dependencies
RUN npm ci --only=production
# Copy the built Nuxt app from the builder stage
COPY --from=builder /app/.output ./.output
# Copy the Prisma schema needed by the Prisma client at runtime
COPY --from=builder /app/prisma/schema.prisma ./prisma/schema.prisma
# Copy the generated Prisma client needed at runtime
COPY --from=builder /app/node_modules/.prisma ./node_modules/.prisma

EXPOSE 3000
# Default command to run the production server
CMD ["node", ".output/server/index.mjs"]
