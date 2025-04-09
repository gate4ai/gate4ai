# github/portal/Dockerfile

# Stage 1: Build Environment
FROM node:lts-alpine AS builder

RUN apk add --no-cache python3 make g++

WORKDIR /app

COPY package.json package-lock.json* ./

COPY prisma ./prisma/

# Allow build scripts to run (e.g., for bcrypt)
# The postinstall script (nuxt prepare && prisma generate) will run here.
RUN npm ci

# Copy the rest of the source code
COPY . .

RUN npx nuxt prepare

ENV NODE_ENV=production
RUN npm run build

# Stage 2: Production Environment
FROM node:lts-alpine

# Needed because the seed script (run by db-init) uses bcrypt from production deps
RUN apk add --no-cache python3 make g++

WORKDIR /app

ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000

COPY --from=builder /app/.output ./.output

COPY package.json package-lock.json* ./

# --- COPY prisma BEFORE npm ci ---
COPY --from=builder /app/prisma ./prisma

# Install ONLY production dependencies, but allow build scripts
# The postinstall script (nuxt prepare && prisma generate) will run here.
RUN npm ci --only=production

EXPOSE 3000

CMD ["node", ".output/server/index.mjs"]