# gate4.ai - Gateway

This directory contains the core MCP Gateway component of the gate4.ai platform, built with Go.

## Purpose

The Gateway serves as the central intermediary between clients (like the Portal or external applications using API keys) and backend MCP servers. Its main responsibilities include:

*   **Authentication & Authorization:** Validating API keys and user credentials based on database configuration. Determining user roles and permissions.
*   **Request Routing:** Forwarding incoming MCP requests to the appropriate backend MCP server(s) based on user subscriptions.
*   **Response Aggregation:** Collecting responses from multiple backend servers (for list operations like `tools/list`, `resources/list`) and merging them into a single response for the client. Handles potential naming conflicts by prefixing resource/tool names with the server ID.
*   **Subscription Management:** Managing client subscriptions to resource updates from backend servers and proxying notifications back to the appropriate clients.
*   **Protocol Handling:** Implements the Model Context Protocol (MCP) for communication.
*   **Proxying (Optional):** Can be configured to proxy requests to the frontend Portal application.

## Technology Stack

*   **Language:** Go (Golang)
*   **Key Libraries:**
    *   Standard Go libraries (`net/http`, `encoding/json`, etc.)
    *   `go.uber.org/zap` (Logging)
    *   `github.com/lib/pq` (PostgreSQL driver)
    *   Internal `shared` and `server` modules.

## Setup & Installation

1.  **Prerequisites:**
    *   Go (version 1.21+)
    *   Access to a running PostgreSQL database configured with the necessary schema (see `portal/prisma/schema.prisma`).

2.  **Environment Variables:**
    *   `GATE4AI_DATABASE_URL`: **Required.** The connection string for the PostgreSQL database containing configuration (users, servers, settings, etc.).
        ```bash
        export GATE4AI_DATABASE_URL="postgresql://user:password@host:port/database?sslmode=disable"
        ```
    *   Other settings (like listen address, log level) are read from the database via the `Settings` table.

## Building

```bash
# From the gate4ai root directory
go build -o gateway_app ./gateway/cmd/main.go

# Or build specifically for Linux (e.g., for Docker)
# CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o gateway_app_linux ./gateway/cmd/main.go
```

## Running

*   **Directly:**
    ```bash
    # Ensure GATE4AI_DATABASE_URL is set
    ./gateway_app
    ```
    The gateway will listen on the address specified in the `gateway_listen_address` setting in the database (defaults typically to `:8080`).

*   **Docker:**
    Use the provided `gateway/Dockerfile` to build a container image.
    ```bash
    # From the gate4ai root directory
    docker build -t gate4ai-gateway -f gateway/Dockerfile .
    docker run -d -p 8080:8080 --name gate4ai-gw \
      -e GATE4AI_DATABASE_URL="your_database_connection_string" \
      gate4ai-gateway
    ```
    See the root `README.md` and `docker-compose.yml` for more advanced Docker usage.

## Configuration

The Gateway primarily reads its configuration from the PostgreSQL database (`Settings` table). Key settings include:

*   `gateway_listen_address`: The address and port to listen on (e.g., `:8080`).
*   `gateway_log_level`: Logging level (e.g., `debug`, `info`, `warn`, `error`).
*   `gateway_authorization_type`: Controls how authorization is handled (`0` for UsersOnly, `1` for MarkedMethods, `2` for None).
*   `gateway_reload_every_seconds`: How often to reload configuration from the DB.
*   `gateway_frontend_address_for_proxy`: URL of the frontend portal if proxying is enabled.

User authentication (API keys) and server definitions are also managed via the database.

## API

The Gateway exposes the standard MCP endpoint, typically at `/mcp` (or `/sse` for v2024 compatibility). Client applications interact with this endpoint using API keys obtained from the Portal.