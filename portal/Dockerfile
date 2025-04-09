# github/portal/Dockerfile

# Stage 1: Build Environment
FROM node:lts-alpine AS builder

# --- ADD Build Tools for Native Addons ---
# Install Python, Make, and C++ compiler needed by node-gyp (used by bcrypt)
RUN apk add --no-cache python3 make g++

WORKDIR /app

COPY package.json package-lock.json* ./

# Allow build scripts to run (e.g., for bcrypt)
RUN npm ci

COPY prisma ./prisma/
# Generate client here if needed by build step, otherwise might move to final stage
RUN npx prisma generate

COPY . .

RUN npx nuxt prepare

ENV NODE_ENV=production
RUN npm run build

# Stage 2: Production Environment
FROM node:lts-alpine

# Install Python, Make, and C++ compiler needed by node-gyp (used by bcrypt)
# If bcrypt was only a devDependency, these might not be needed here.
# But since seed script runs in prod-like init container, we need it.
RUN apk add --no-cache python3 make g++

WORKDIR /app

ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000

COPY --from=builder /app/.output ./.output

COPY package.json package-lock.json* ./

# Install ONLY production dependencies, but allow build scripts
RUN npm ci --only=production

COPY --from=builder /app/prisma ./prisma
# Explicitly generate the Prisma client in the final stage
RUN npx prisma generate

EXPOSE 3000

# CMD remains the same
CMD ["node", ".output/server/index.mjs"]