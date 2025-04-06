# All-in-one Dockerfile for gate4.ai
# This Dockerfile builds and runs the www (www), gateway gateway, and database

# ------------------------------------------------------------
# Stage 1: Build Portal (Nuxt.js Frontend)
# ------------------------------------------------------------
FROM node:lts-alpine AS portal-builder

WORKDIR /app/portal

COPY portal/package.json portal/package-lock.json* ./
RUN npm ci --ignore-scripts

COPY portal/prisma ./prisma/
COPY portal/ ./

# Ensure DATABASE_URL is set if generate needs it, otherwise remove the ARG/ENV
# ARG DATABASE_URL
# ENV DATABASE_URL=${DATABASE_URL}
RUN npx prisma generate
RUN npx nuxt prepare

ENV NODE_ENV=production
RUN npm run build

# ------------------------------------------------------------
# Stage 2: Build Gateway (Go Backend)
# ------------------------------------------------------------
FROM golang:1.24-alpine AS gateway-builder

WORKDIR /app

# Copy shared and server first if they exist at the root
COPY shared ./shared
COPY server ./server

# Copy gateway source and mod files
COPY gateway/go.mod gateway/go.sum ./gateway/
COPY gateway ./gateway

WORKDIR /app/gateway

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /gateway-app ./cmd/main.go

# ------------------------------------------------------------
# Stage 3: Final Image
# ------------------------------------------------------------
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies: nodejs for Portal, postgresql-client for potential DB checks/init
# dumb-init for better signal handling
RUN apk add --no-cache nodejs npm postgresql-client dumb-init ca-certificates

# Create non-root user? (Optional but recommended for security)
# RUN addgroup -S appgroup && adduser -S appuser -G appgroup
# USER appuser

# Copy built Portal artifacts
COPY --from=portal-builder /app/portal/.output ./portal/.output
# Copy Portal production dependencies (or install them)
COPY --from=portal-builder /app/portal/node_modules ./portal/node_modules
COPY portal/package.json portal/package-lock.json* ./portal/
# Optional: RUN cd portal && npm ci --only=production --ignore-scripts
# Copy Prisma schema needed by Portal runtime
COPY --from=portal-builder /app/portal/prisma ./portal/prisma

# Copy built Gateway artifact
COPY --from=gateway-builder /gateway-app ./gateway/gateway-app

# Create startup script (adjust paths and commands as needed)
# NOTE: This script assumes DATABASE_URL is set correctly for both Portal and Gateway.
# It does NOT run Postgres inside this container; use Docker Compose for that.
RUN echo '#!/bin/sh' > /app/start.sh && \
    echo 'set -e' >> /app/start.sh && \
    echo '' >> /app/start.sh && \
    echo '# Wait for database (if external, optional check)' >> /app/start.sh && \
    echo '# Example: while ! pg_isready -h ${DB_HOST:-postgres} -p ${DB_PORT:-5432} -U ${DB_USER:-postgres}; do echo "Waiting for database..."; sleep 2; done' >> /app/start.sh && \
    echo '' >> /app/start.sh && \
    echo '# Start Portal server in background' >> /app/start.sh && \
    echo 'echo "Starting Portal..."' >> /app/start.sh && \
    echo 'cd /app/portal' >> /app/start.sh && \
    echo 'node .output/server/index.mjs &' >> /app/start.sh && \
    echo 'PORTAL_PID=$!' >> /app/start.sh && \
    echo 'echo "Portal started (PID: $PORTAL_PID) on port 3000"' >> /app/start.sh && \
    echo '' >> /app/start.sh && \
    echo '# Start Gateway server in background' >> /app/start.sh && \
    echo 'echo "Starting Gateway..."' >> /app/start.sh && \
    echo 'cd /app/gateway' >> /app/start.sh && \
    echo './gateway-app &' >> /app/start.sh && \
    echo 'GATEWAY_PID=$!' >> /app/start.sh && \
    echo 'echo "Gateway started (PID: $GATEWAY_PID) on port 8080"' >> /app/start.sh && \
    echo '' >> /app/start.sh && \
    echo '# Wait for processes to exit' >> /app/start.sh && \
    echo 'wait $PORTAL_PID $GATEWAY_PID' >> /app/start.sh && \
    echo 'EXIT_CODE=$?' >> /app/start.sh && \
    echo "Processes finished with exit code $EXIT_CODE" >> /app/start.sh && \
    echo 'exit $EXIT_CODE' >> /app/start.sh && \
    chmod +x /app/start.sh

# Expose ports
EXPOSE 3000 8080

# Default environment variables (can be overridden)
ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000
# GATE4AI_DATABASE_URL needs to be provided externally (e.g., Docker run -e or Compose)
# Example: ENV GATE4AI_DATABASE_URL="postgresql://user:password@db:5432/gate4ai?sslmode=disable"

# Use dumb-init as the entrypoint to handle signals properly
ENTRYPOINT ["/usr/bin/dumb-init", "--"]

# Run the startup script
CMD ["/app/start.sh"] 