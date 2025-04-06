# Stage 1: Build the Go application
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build tools if necessary (e.g., git)
# RUN apk add --no-cache git

# Copy go module files first to leverage Docker cache
COPY go.mod go.sum ./
# Copy dependency directories if using replace directives
COPY ../shared ./../shared
COPY ../server ./../server

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go application
# Use CGO_ENABLED=0 for a static binary (usually smaller and avoids C dependencies)
# Use -ldflags="-w -s" to strip debug symbols and reduce binary size
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /gateway-app ./cmd/main.go

# Stage 2: Create the final minimal image
FROM alpine:latest

# Install any necessary runtime dependencies (e.g., ca-certificates for HTTPS)
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /gateway-app /app/gateway-app

# Expose the port the gateway listens on (adjust if different)
# This should match the 'gateway_listen_address' setting in the DB
EXPOSE 8080

# Set the entrypoint to the compiled binary
ENTRYPOINT ["/app/gateway-app"]

# Default command can be empty if entrypoint is sufficient
# CMD [""] 