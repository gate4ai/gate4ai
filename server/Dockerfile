#
# !!!!!!!!! Build from parent directory !!!!!!!!!
#
# cd .. &&  docker build -f server/Dockerfile .
#

FROM golang:1.24-alpine AS builder

WORKDIR /app


COPY ./shared  ./shared
COPY ./server  ./server
COPY ./tests   ./tests

WORKDIR /app/server

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o example-server ./cmd/startExample.go

# Stage 2: Create the final minimal image
FROM alpine:latest

# Install any necessary runtime dependencies (e.g., ca-certificates for HTTPS)
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /app/server/example-server /app/example-server

COPY ./server/cmd/mcp-example-server/config.yaml /app/config.yaml

EXPOSE 4000

# Set the entrypoint to the compiled binary
ENTRYPOINT ["/app/example-server"]
CMD ["--config", "/app/config.yaml"] 
