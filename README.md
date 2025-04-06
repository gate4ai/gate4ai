# gate4.ai - Enterprise MCP Gateway

**gate4.ai** is a comprehensive platform designed to manage and secure access to [Model Context Protocol (MCP)](https://github.com/modelcontextprotocol/specification) servers and provide a unified interface for various AI/ML models. It acts as a central gateway, handling user authentication, authorization, server discovery, request routing, and potentially proxying access to its own management UI and API.

## General Architecture

```svg
<svg width="750" height="400" viewBox="0 0 750 400" xmlns="http://www.w3.org/2000/svg" font-family="Arial, sans-serif" font-size="13">
  <defs>
    <marker id="arrow_ag" viewBox="0 0 10 10" refX="5" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
      <path d="M 0 0 L 10 5 L 0 10 z" fill="#333" />
    </marker>
  </defs>
  <rect x="20" y="80" width="100" height="60" rx="5" fill="#e0f7fa" stroke="#00796b"/>
  <text x="70" y="115" text-anchor="middle">User</text>
  <rect x="20" y="180" width="100" height="60" rx="5" fill="#e0f7fa" stroke="#00796b"/>
  <text x="70" y="215" text-anchor="middle">API Client</text>
  <rect x="590" y="150" width="140" height="100" rx="5" fill="#fffde7" stroke="#fbc02d"/>
  <text x="660" y="190" text-anchor="middle">Backend Server 1</text>
  <text x="660" y="210" text-anchor="middle">(MCP)</text>
  <text x="660" y="230" text-anchor="middle">...</text>
  <rect x="180" y="100" width="160" height="200" rx="8" fill="#dcedc8" stroke="#689f38" stroke-width="1.5"/>
  <text x="260" y="130" text-anchor="middle" font-weight="bold">Gateway (Go)</text>
  <text x="260" y="160" text-anchor="middle">- Entry Point</text>
  <text x="260" y="180" text-anchor="middle">- Auth (API Key)</text>
  <text x="260" y="200" text-anchor="middle">- Routing</text>
  <text x="260" y="220" text-anchor="middle">- MCP Aggregation</text>
  <text x="260" y="240" text-anchor="middle">- Proxy (UI/API)</text>
  <text x="260" y="260" text-anchor="middle">- Status Check</text>
  <text x="260" y="280" text-anchor="middle">- DB Access (Config)</text>
  <rect x="370" y="20" width="180" height="140" rx="8" fill="#e1f5fe" stroke="#0288d1" stroke-width="1.5"/>
  <text x="460" y="45" text-anchor="middle" font-weight="bold">Portal (Nuxt.js)</text>
  <text x="460" y="70" text-anchor="middle">- UI Frontend</text>
  <text x="460" y="90" text-anchor="middle">- Backend API (/api)</text>
  <text x="460" y="110" text-anchor="middle">- User/Sub/Key/Server Mgmt</text>
  <text x="460" y="130" text-anchor="middle">- DB Access (Prisma)</text>
  <ellipse cx="460" cy="260" rx="90" ry="45" fill="#f3e5f5" stroke="#7b1fa2" stroke-width="1.5"/>
  <text x="460" y="265" text-anchor="middle" font-weight="bold">Database</text>
  <text x="460" y="285" text-anchor="middle">(PostgreSQL)</text>
  <path d="M 120 110 Q 150 110, 180 150" stroke="#333" stroke-width="1.5" fill="none" marker-end="url(#arrow_ag)"/>
  <text x="125" y="140" font-size="11">HTTPS (UI Req)</text>
  <path d="M 120 210 Q 150 210, 180 250" stroke="#333" stroke-width="1.5" fill="none" marker-end="url(#arrow_ag)"/>
  <text x="125" y="240" font-size="11">MCP/API (Key Auth)</text>
  <path d="M 340 140 H 370" stroke="#333" stroke-width="1.5" fill="none" marker-end="url(#arrow_ag)" stroke-dasharray="4,4"/>
  <text x="345" y="135" font-size="11">Proxy UI/API Req</text>
  <path d="M 460 160 V 215" stroke="#333" stroke-width="1.5" fill="none" marker-end="url(#arrow_ag)"/>
  <text x="470" y="190" font-size="11">DB (Prisma)</text>
  <path d="M 340 260 Q 380 260, 400 235" stroke="#333" stroke-width="1.5" fill="none" marker-end="url(#arrow_ag)"/>
  <text x="350" y="245" font-size="11">DB (Go)</text>
  <path d="M 340 200 H 590" stroke="#333" stroke-width="1.5" fill="none" marker-end="url(#arrow_ag)"/>
  <text x="450" y="195" font-size="11">MCP Call</text>
</svg>
```

## Key Features

*   **Centralized Gateway:** Acts as a single entry point for MCP requests and optionally proxies the management Portal UI/API.
*   **User & API Key Management:** Securely manage users and API keys with role-based access control (RBAC) via the Portal.
*   **Server Catalog:** Discover, register, and manage available backend MCP servers (public, private, subscription-based).
*   **Subscription Management:** Control user access to specific servers through subscriptions (pending, active, blocked states).
*   **Request Routing & Aggregation:** Routes MCP requests to appropriate backend servers based on user subscriptions and handles aggregation for list operations.
*   **Detailed Logging:** Tracks tool calls and server interactions for auditing and debugging (stored in the database).
*   **Web Portal:** User-friendly interface (UI and API) for managing servers, users, keys, subscriptions, and settings. Accessed via the Gateway's proxy.

## Components

1.  **Gateway (`./gateway`):** The core Go application. Handles all incoming requests, performs authentication (API keys), routes MCP calls to backend servers, proxies requests to the Portal, reads configuration, and logs activities.
2.  **Portal (`./portal`):** A Nuxt.js web application providing the UI and backend API (`/api/...`) for administration and user interaction (user/key/server/subscription management). Interacts with the Database via Prisma. Accessed *through* the Gateway.
3.  **Example Server (`./server`):** A sample Go MCP server implementation used for testing and demonstration. Represents a potential backend service.
4.  **Shared Library (`./shared`):** Common Go code (interfaces, MCP definitions, config helpers) used by Gateway and Example Server.
5.  **Tests (`./tests`):** End-to-end (Playwright) and integration tests (Go).

## Technology Stack

*   **Gateway:** Go
*   **Portal:** Nuxt.js 3 (Vue.js 3), Vuetify 3, TypeScript, Prisma (ORM)
*   **Example Server:** Go
*   **Database:** PostgreSQL
*   **Testing:** Playwright (E2E), Go testing library, Testcontainers
*   **Containerization:** Docker, Docker Compose
*   **CI/CD:** GitHub Actions

## Project Structure

```svg
<svg width="450" height="700" viewBox="0 0 450 700" xmlns="http://www.w3.org/2000/svg" font-family="monospace" font-size="14">
  <text x="10" y="25" font-weight="bold">gate4ai/mcp/</text>
  <text x="30" y="50">├── .github/</text>
  <text x="50" y="70">│   └── workflows/</text>
  <text x="70" y="90">│       └── ci.yml        (CI Pipeline)</text>
  <text x="30" y="110">├── portal/             (Frontend UI & Backend API)</text>
  <text x="50" y="130">│   ├── components/   (Vue Components)</text>
  <text x="50" y="150">│   ├── pages/        (Nuxt Pages)</text>
  <text x="50" y="170">│   ├── server/       (Nuxt API Routes)</text>
  <text x="70" y="190">│   │   ├── api/      (API Endpoints)</text>
  <text x="70" y="210">│   │   ├── middleware/</text>
  <text x="70" y="230">│   │   └── utils/    (Server Utils)</text>
  <text x="50" y="250">│   ├── prisma/       (DB Schema, Migrations, Seed)</text>
  <text x="70" y="270">│   │   └── schema.prisma</text>
  <text x="50" y="290">│   ├── plugins/      (Nuxt Plugins - Vuetify, Auth, API, Settings)</text>
  <text x="50" y="310">│   ├── public/       (Static Assets)</text>
  <text x="50" y="330">│   ├── nuxt.config.ts</text>
  <text x="50" y="350">│   ├── package.json</text>
  <text x="50" y="370">│   └── Dockerfile</text>
  <text x="30" y="390">├── gateway/            (MCP Gateway - Go)</text>
  <text x="50" y="410">│   ├── cmd/main.go   (Entrypoint)</text>
  <text x="50" y="430">│   ├── capability/   (MCP Method Handlers)</text>
  <text x="50" y="450">│   ├── client/       (MCP Client for Backends)</text>
  <text x="50" y="470">│   ├── extra/        (Proxy, Info Handler)</text>
  <text x="50" y="490">│   ├── go.mod</text>
  <text x="50" y="510">│   └── Dockerfile</text>
  <text x="30" y="530">├── server/             (Example MCP Server - Go)</text>
  <text x="50" y="550">│   ├── cmd/          (Entrypoint, Config)</text>
  <text x="50" y="570">│   └── Dockerfile</text>
  <text x="30" y="590">├── shared/             (Shared Go Code)</text>
  <text x="50" y="610">│   ├── config/       (Config Interfaces/Impl)</text>
  <text x="50" y="630">│   └── mcp/          (MCP Schema Structs)</text>
  <text x="30" y="650">├── tests/              (Integration & E2E Tests)</text>
  <text x="50" y="670">│   └── *.go          (Test Files)</text>
  <text x="30" y="690">├── .gitignore</text>
  <text x="30" y="710">├── README.md</text>
  <text x="30" y="730">├── docker-compose.yml</text>
  <text x="30" y="750">├── all-in-one.dockerfile (Demo only)</text>
  <text x="30" y="770">└── LICENSE</text>
  <svg height="790"/>
</svg>
```

## Getting Started

### Prerequisites

*   Go (version 1.21+)
*   Node.js (LTS version recommended)
*   npm
*   Docker & Docker Compose
*   (Optional for E2E tests) Playwright browsers (`npx playwright install --with-deps` in `./tests` or `./portal`)

### Recommended Setup (Docker Compose)

This is the easiest way to get all services running locally.

1.  **Clone:** `git clone https://github.com/gate4ai/mcp.git && cd mcp`
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

Contributions are welcome! Please refer to `CONTRIBUTING.md` (if available) or standard GitHub practices (fork, branch, PR).

