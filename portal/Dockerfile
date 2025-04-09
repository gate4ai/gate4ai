# github/portal/Dockerfile
# Stage 1: Build Environment
FROM node:lts-alpine AS builder

WORKDIR /app

COPY package.json package-lock.json* ./

RUN npm ci --ignore-scripts

COPY prisma ./prisma/

# Generate client in builder stage (still useful for Nuxt build potentially)
RUN npx prisma generate

COPY . .

RUN npx nuxt prepare

ENV NODE_ENV=production
RUN npm run build

# Stage 2: Production Environment
FROM node:lts-alpine

WORKDIR /app

ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000

COPY --from=builder /app/.output ./.output

COPY package.json package-lock.json* ./
# Install ONLY production dependencies
RUN npm ci --only=production --ignore-scripts

COPY --from=builder /app/prisma ./prisma

# Explicitly generate the Prisma client in the final stage
RUN npx prisma generate

EXPOSE 3000

CMD ["node", ".output/server/index.mjs"]