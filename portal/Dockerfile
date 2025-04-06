# Stage 1: Build Environment
FROM node:lts-alpine AS builder

WORKDIR /app

# Copy dependency definition files
COPY package.json package-lock.json* ./

# Install dependencies
# Use --ignore-scripts initially if prisma generate is problematic here
RUN npm ci --ignore-scripts

# Copy prisma schema and migrations *before* installing source
# This leverages Docker cache better if only source code changes
COPY prisma ./prisma/

# Copy the rest of the application source code
COPY . .

# Generate Prisma client explicitly now that source code is present
# Ensure the database URL is available during build *if needed* by generate,
# otherwise, it's only needed at runtime. Often, generate doesn't need a live DB.
# If it DOES need a live DB for introspection, this build stage becomes more complex.
# Assuming generate works offline based on schema:
RUN npx prisma generate

# Prepare Nuxt application (compiles types, etc.)
RUN npx nuxt prepare

# Build the Nuxt application for production
# Set NODE_ENV to production for optimized build
ENV NODE_ENV=production
RUN npm run build

# Optional: Prune development dependencies (if space is critical)
# RUN npm prune --production

# Stage 2: Production Environment
FROM node:lts-alpine

WORKDIR /app

# Set NODE_ENV to production for runtime
ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000

# Copy built Nuxt application output
COPY --from=builder /app/.output ./.output

# Copy production node_modules (if pruned in builder stage)
# COPY --from=builder /app/node_modules ./node_modules
# OR Install only production dependencies
COPY package.json package-lock.json* ./
RUN npm ci --only=production --ignore-scripts

# Copy Prisma schema needed at runtime (e.g., by the server routes)
COPY --from=builder /app/prisma ./prisma

# Expose the port the app runs on
EXPOSE 3000

# Command to run the application
# Nuxt 3 production server is started via .output/server/index.mjs
CMD ["node", ".output/server/index.mjs"] 