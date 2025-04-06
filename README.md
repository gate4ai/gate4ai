# gate4.ai - Enterprise MCP Gateway

**gate4.ai** is a comprehensive platform designed to manage and secure access to [Model Context Protocol (MCP)](https://github.com/modelcontextprotocol/specification) servers. It provides a central point of control for user authentication, authorization, server discovery, and request routing.

## Key Features

*   **Centralized Gateway:** Acts as a single entry point for accessing multiple backend MCP servers.
*   **User & API Key Management:** Securely manage users and API keys with role-based access control (RBAC).
*   **Server Catalog:** Discover and manage available MCP servers (both public and private).
*   **Subscription Management:** Control user access to specific servers through subscriptions (pending, active, blocked states).
*   **Request Aggregation:** Route requests to the appropriate backend server based on user subscriptions and available tools/resources.
*   **Detailed Logging:** Track tool calls and server interactions for auditing and debugging.
*   **Web Portal:** User-friendly interface for managing servers, users, keys, and subscriptions.

## Components

1.  **Portal (`./portal`):** A Nuxt.js web application providing the user interface for administration and user interaction. Includes Prisma for database interaction.
2.  **Gateway (`./gateway`):** The core Go application acting as the MCP gateway. Handles authentication, routing, and aggregation.
3.  **Example Server (`./server`):** A sample Go MCP server implementation demonstrating how to build services compatible with the gateway.
4.  **Shared Library (`./shared`):** Common Go code used by the Gateway and Example Server, including MCP definitions and configuration handling.
5.  **Tests (`./tests`):** End-to-end and integration tests using Go and Playwright.

## Technology Stack

*   **Backend (Gateway, Example Server):** Go
*   **Frontend (Portal):** Nuxt.js 3 (Vue.js 3), Vuetify 3, TypeScript
*   **Database:** PostgreSQL
*   **ORM:** Prisma (within the Portal component)
*   **Testing:** Playwright (E2E), Go testing library
*   **Containerization:** Docker

## Getting Started

### Prerequisites

*   Go (version 1.21+)
*   Node.js (LTS version recommended, check `./portal/package.json` for specifics)
*   npm (usually comes with Node.js)
*   Docker & Docker Compose (for containerized deployment)
*   PostgreSQL Server (running locally or via Docker)

### Local Development Setup

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/gate4ai/mcp.git
    cd mcp
    ```

2.  **Set up PostgreSQL:**
    *   Start a PostgreSQL container (see `postgresql.md` for an example command) or use an existing instance.
    *   Create a database (e.g., `gate4ai`).
    *   Set the `GATE4AI_DATABASE_URL` environment variable:
        ```bash
        export GATE4AI_DATABASE_URL="postgresql://user:password@host:port/database?sslmode=disable"
        # Example for default Docker setup:
        # export GATE4AI_DATABASE_URL="postgresql://postgres:password@localhost:5432/gate4ai?sslmode=disable"
        ```

3.  **Set up Portal:**
    ```bash
    cd portal
    npm install
    npx prisma migrate dev --name init # Apply migrations & generate client
    npx prisma db seed           # Seed initial data (admin user, settings)
    npm run dev                 # Start dev server (usually on http://localhost:3000)
    cd ..
    ```

4.  **Set up Gateway:**
    *   Ensure the database settings reflect the correct URLs (the seed script sets defaults, edit if needed via Portal UI or directly).
    *   Run the gateway:
        ```bash
        go run ./gateway/cmd/main.go
        # Or build and run:
        # go build -o gateway_app ./gateway/cmd/main.go
        # ./gateway_app
        ```
    *   The gateway usually runs on port 8080 by default (check `gateway_listen_address` setting).

5.  **Set up Example Server (Optional):**
    ```bash
    go run ./server/cmd/startExample.go --port 4001 # Or build and run
    ```

6.  **Access:**
    *   Portal: `http://localhost:3000` (or configured port)
    *   Gateway MCP Endpoint: `http://localhost:8080/mcp` (or configured port/path)
    *   Example Server MCP Endpoint: `http://localhost:4001/sse` (or configured port/path)

## Contributing

Contributions are welcome!
