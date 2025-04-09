# Stage 1: Build Environment
FROM node:lts-alpine AS builder

WORKDIR /app

COPY package.json package-lock.json* ./

RUN npm ci --ignore-scripts

COPY prisma ./prisma/

COPY . .

RUN npx prisma generate

RUN npx nuxt prepare

ENV NODE_ENV=production
RUN npm run build

# Stage 2: Production Environment
FROM node:lts-alpine

WORKDIR /app

ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000

# --> ADDED: Install postgresql-client for pg_isready in the entrypoint script
RUN apk add --no-cache postgresql-client

COPY --from=builder /app/.output ./.output

COPY package.json package-lock.json* ./
RUN npm ci --only=production --ignore-scripts

COPY --from=builder /app/prisma ./prisma

# --> ADDED: Copy and set permissions for the entrypoint script
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

EXPOSE 3000

# --> ADDED: Set the entrypoint script
ENTRYPOINT ["/app/docker-entrypoint.sh"]

# --> Keep the original CMD, it will be passed to the entrypoint script
CMD ["node", ".output/server/index.mjs"]