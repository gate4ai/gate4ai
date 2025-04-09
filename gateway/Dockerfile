#
# !!!!!!!!! Build from parent directory !!!!!!!!!
#
# cd .. &&  docker build -f gateway/Dockerfile .
#

FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY ./gateway ./gateway
COPY ./shared  ./shared
COPY ./server  ./server
COPY ./tests   ./tests

WORKDIR /app/gateway

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o gateway ./cmd/main.go

# Stage 2: Create the final minimal image
FROM alpine:latest

# Install any necessary runtime dependencies (e.g., ca-certificates for HTTPS)
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /app/gateway/gateway /app/gateway



EXPOSE 4080

# Set the entrypoint to the compiled binary
ENTRYPOINT ["/app/gateway"]
