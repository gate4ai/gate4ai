# gate4.ai - Enterprise MCP Gateway

**gate4.ai** is a comprehensive platform designed to manage and secure access to [Model Context Protocol (MCP)](https://github.com/modelcontextprotocol/specification) servers and provide a unified interface for various AI/ML models. It acts as a central gateway, handling user authentication, authorization, server discovery, request routing, and potentially proxying access to its own management UI and API.

## General Architecture

![general-architecture](http://docs.gate4.ai/diagrams/general-architecture.png)

## Key Features

*   **Centralized Gateway:** Acts as a single entry point for MCP requests and optionally proxies the management Portal UI/API.
*   **Web Portal:** User-friendly interface (UI and API) for managing servers, users, keys, subscriptions, and settings. Accessed via the Gateway's proxy.
*   **Server Catalog:** Discover, register, and manage available backend MCP and REST servers (public, private, subscription-based).
*   **User & API Key Management:** Securely manage users and API keys with role-based access control (RBAC) via the Portal.
*   **Subscription Management:** Control user access to specific servers through subscriptions (pending, active, blocked states).
*   **Request Routing & Aggregation:** Routes MCP requests to appropriate backend servers based on user subscriptions and handles aggregation for list operations.
*   **Detailed Logging:** Tracks tool calls and server interactions for auditing and debugging.

## Components

1.  **Gateway (`./gateway`):** The core Go application. Handles all incoming requests, performs authentication (API keys), routes MCP calls to backend servers, proxies requests to the Portal, reads configuration, and logs activities.
2.  **Portal (`./portal`):** A Nuxt.js web application providing the UI and backend API (`/api/...`) for administration and user interaction (user/key/server/subscription management). Interacts with the Database via Prisma. Accessed *through* the Gateway.
3.  **Server (`./server`):** MCP server implementation used for implementing custom MCP servers. It is used as a backend in the gateway, and also hosts the MCP servers provided by us. It is separable and can serve as a development framework for third-party MCP servers.
4.  **Shared Library (`./shared`):** Common Go code (interfaces, MCP definitions, config helpers) used by Gateway and Server.
5.  **Tests (`./tests`):** End-to-end (Playwright) and integration tests (Go).

## Technology Stack

*   **Gateway:** Go
*   **Portal:** Nuxt.js 3 (Vue.js 3), Vuetify 3, TypeScript, Prisma (ORM)
*   **Example Server:** Go
*   **Database:** PostgreSQL
*   **Testing:** Playwright (E2E), Go testing library, Testcontainers
*   **Containerization:** Docker, Docker Compose
*   **CI/CD:** GitHub Actions

## Getting Started

### Prerequisites

*   Go (version 1.21+)
*   Node.js (LTS version recommended)
*   npm
*   Docker & Docker Compose
*   (Optional for E2E tests) Playwright browsers (`npx playwright install --with-deps` in `./tests` or `./portal`)

### Recommended Setup (Docker Compose)

This is the easiest way to get all services running locally.

1.  **Clone:** `git clone https://github.com/gate4ai/gate4ai.git && cd mcp`
2.  **Environment:** Create a `.env` file in the root directory (copy from `.env.example` if provided) and configure `GATE4AI_DATABASE_URL`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `JWT_SECRET`.
    *Example `.env` for local Docker:*
    ```env
    POSTGRES_USER=gate4ai_user
    POSTGRES_PASSWORD=changeme_local
    POSTGRES_DB=gate4ai_db
    # Ensure the hostname 'db' matches the service name in docker-compose.yml
    GATE4AI_DATABASE_URL=postgresql://gate4ai_user:changeme_local@db:5432/gate4ai_db?sslmode=disable
    # Generate a strong secret: node -e "console.log(require('crypto').randomBytes(32).toString('hex'))"
    JWT_SECRET=your_generated_32_byte_hex_secret
    # Optional: Specify ports if defaults are taken
    # PORTAL_PORT=3001
    # GATEWAY_PORT=8081
    # POSTGRES_PORT=5433
    ```
3.  **Build & Start:**
    ```bash
    docker compose up --build -d
    ```
4.  **Database Setup (First Run):** Wait a moment for services to start, then run migrations and seeding *inside the portal container*:
    ```bash
    docker compose exec portal npx prisma migrate dev --name init
    docker compose exec portal npx prisma db seed
    ```
5.  **Access:**
    *   **Portal UI:** Access via the **Gateway's** port (e.g., `http://localhost:8080` if `GATEWAY_PORT` is 8080). The Gateway proxies to the Portal.
    *   **Gateway MCP Endpoint:** `http://localhost:8080/mcp` (or configured port/path)

### Manual Local Development Setup

(Refer to individual component READMEs for details)

1.  **Clone.**
2.  **Setup PostgreSQL** (manually or via Docker) and set `GATE4AI_DATABASE_URL`.
3.  **Setup Portal (`./portal`):** `npm install`, `npx prisma migrate dev`, `npx prisma db seed`, `npm run dev`. Set `JWT_SECRET`.
4.  **Setup Gateway (`./gateway`):** `go run ./gateway/cmd/main.go`. Ensure `GATE4AI_DATABASE_URL` is set.
5.  **Setup Example Server (`./server`):** `go run ./server/cmd/startExample.go --port 4001`.
6.  **Access:** Portal (`http://localhost:3000`), Gateway (`http://localhost:8080/mcp`), Example Server (`http://localhost:4001/sse`). *Note: For full functionality, configure the Gateway in the Portal settings to point to `http://localhost:8080`.*

## Contributing

We greatly welcome contributors and are ready to assist with onboarding. Don't hesitate to ask questions and make suggestions!

## Community & Support

*   **Community Chat:** [https://t.me/gate4ai_chat](https://t.me/gate4ai_chat) - Ask questions, discuss features.
*   **Announcements:** [https://t.me/gate4ai_channel](https://t.me/gate4ai_channel) - Stay updated on releases.
*   **CEO Feedback:** [https://t.me/gate4ai](https://t.me/gate4ai) - Share your thoughts directly with the CEO.
*   **Feedback Email:** feedback@gate4.ai
