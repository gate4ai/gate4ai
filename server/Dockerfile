# Stage 1: Build the Go application
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build tools if necessary (e.g., git)
# RUN apk add --no-cache git

# Copy go module files first to leverage Docker cache
COPY go.mod go.sum ./
# Copy dependency directories if using replace directives
COPY ../shared ./../shared

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go application
# Use CGO_ENABLED=0 for a static binary
# Use -ldflags="-w -s" to strip debug symbols
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /example-server-app ./cmd/startExample.go

# Stage 2: Create the final minimal image
FROM alpine:latest

# Install any necessary runtime dependencies (e.g., ca-certificates)
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /example-server-app /app/example-server-app

# Copy the default config file (optional, can be mounted or passed via env vars)
COPY ./cmd/config.yaml /app/config.yaml

# Expose the default port the example server listens on (adjust if different)
EXPOSE 4001

# Set the entrypoint to the compiled binary
# Pass default config path and let PORT be overridden by environment
ENTRYPOINT ["/app/example-server-app"]
CMD ["--config", "/app/config.yaml"] 