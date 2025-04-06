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

COPY --from=builder /app/.output ./.output

COPY package.json package-lock.json* ./
RUN npm ci --only=production --ignore-scripts

COPY --from=builder /app/prisma ./prisma

EXPOSE 3000

CMD ["node", ".output/server/index.mjs"] 
