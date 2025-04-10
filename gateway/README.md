# gate4.ai - Gateway

This directory contains the core MCP Gateway component of the gate4.ai platform, built with Go.

## Purpose

The Gateway serves as the central entry point and intermediary for all interactions with the gate4.ai platform. Its main responsibilities include:

*   **Request Handling:** Receives all incoming HTTP requests.
*   **Authentication:** Validates API keys for direct MCP/API calls. For UI interactions, relies on the Portal's authentication (e.g., JWT validated by the Portal's API routes).
*   **Routing:**
    *   **MCP Requests (`/mcp`):** Forwards valid MCP requests to the appropriate backend MCP server(s) based on user subscriptions and configuration.
    *   **Portal UI/API Requests (`/`):** Proxies requests to the internal Portal (Nuxt.js) service.
    *   **Status Requests (`/status`):** Handles health checks internally.
*   **MCP Aggregation:** Collects responses from multiple backend servers (for list operations like `tools/list`) and merges them.
*   **Configuration Loading:** Reads its primary configuration (listen address, log level, backend server URLs, API key hashes) from the Database or YAML.
*   **Logging:** Records key events and potentially tool calls to the Database.

## Detailed Architecture (Gateway Focus)

```svg
<svg width="850" height="550" viewBox="0 0 850 550" xmlns="http://www.w3.org/2000/svg" font-family="Arial, sans-serif" font-size="12">
  <defs>
    <marker id="arrow_ad" viewBox="0 0 10 10" refX="5" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
      <path d="M 0 0 L 10 5 L 0 10 z" fill="#444" />
    </marker>
  </defs>
  <g transform="translate(20, 150)">
      <rect x="0" y="0" width="120" height="50" rx="5" fill="#e3f2fd" stroke="#90caf9"/>
      <text x="60" y="30" text-anchor="middle">Browser (UI)</text>
  </g>
  <g transform="translate(20, 250)">
      <rect x="0" y="0" width="120" height="50" rx="5" fill="#c8e6c9" stroke="#a5d6a7"/>
      <text x="60" y="30" text-anchor="middle">Client App (API Key)</text>
  </g>
  <rect x="160" y="10" width="670" height="530" rx="10" fill="none" stroke="#bdbdbd" stroke-dasharray="5,5"/>
  <text x="170" y="30" font-style="italic" fill="#555">Docker Network</text>
  <rect x="180" y="50" width="200" height="450" rx="5" fill="#dcedc8" stroke="#a5d6a7"/>
  <text x="280" y="75" text-anchor="middle" font-weight="bold">Gateway Container (Go)</text>
  <line x1="190" y1="90" x2="370" y2="90" stroke="#a5d6a7"/>
  <text x="280" y="115" text-anchor="middle">HTTP Transport</text>
  <text x="280" y="135" text-anchor="middle">(Listens on Port X)</text>
  <text x="280" y="165" text-anchor="middle">Router (/mcp, /, /status)</text>
  <text x="280" y="195" text-anchor="middle">Auth Manager (Keys)</text>
  <text x="280" y="225" text-anchor="middle">Proxy Logic</text>
  <text x="280" y="255" text-anchor="middle">MCP Session Manager</text>
  <text x="280" y="285" text-anchor="middle">MCP Capability</text>
  <text x="280" y="310" text-anchor="middle">   - Backend Client</text>
  <text x="280" y="335" text-anchor="middle">DB Access (Config/Logs)</text>
  <text x="280" y="365" text-anchor="middle">Status Handler</text>
  <rect x="400" y="50" width="200" height="200" rx="5" fill="#e1f5fe" stroke="#81d4fa"/>
  <text x="500" y="75" text-anchor="middle" font-weight="bold">Portal Container</text>
  <line x1="410" y1="90" x2="590" y2="90" stroke="#81d4fa"/>
  <text x="500" y="115" text-anchor="middle">Nuxt.js Server</text>
  <text x="500" y="140" text-anchor="middle">- API Routes (/api)</text>
  <text x="500" y="165" text-anchor="middle">- Auth (JWT)</text>
  <text x="500" y="190" text-anchor="middle">- Management Logic</text>
  <text x="500" y="215" text-anchor="middle">Prisma Client</text>
  <rect x="400" y="270" width="200" height="80" rx="5" fill="#f3e5f5" stroke="#ce93d8"/>
  <text x="500" y="295" text-anchor="middle" font-weight="bold">Database Container</text>
  <text x="500" y="320" text-anchor="middle">PostgreSQL</text>
  <rect x="620" y="180" width="200" height="80" rx="5" fill="#fffde7" stroke="#fff59d"/>
  <text x="720" y="205" text-anchor="middle" font-weight="bold">Backend Server 1</text>
  <text x="720" y="230" text-anchor="middle">(Go MCP)</text>
  <rect x="620" y="280" width="200" height="80" rx="5" fill="#fffde7" stroke="#fff59d"/>
  <text x="720" y="305" text-anchor="middle" font-weight="bold">Backend Server N</text>
  <text x="720" y="330" text-anchor="middle">(MCP)</text>
  <path d="M 140 175 H 180" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)"/>
  <text x="145" y="170" font-size="10">HTTPS</text>
  <path d="M 140 275 H 180" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)"/>
  <text x="145" y="270" font-size="10">HTTPS/MCP</text>
  <path d="M 380 120 H 400" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)" stroke-dasharray="4,4"/>
  <text x="370" y="110" font-size="10">Proxy Req</text>
  <path d="M 400 130 H 380" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)" stroke-dasharray="4,4"/>
  <text x="370" y="145" font-size="10">Proxy Resp</text>
  <path d="M 500 250 V 270" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)"/>
  <text x="510" y="265" font-size="10">DB Query</text>
  <path d="M 380 305 Q 390 305, 400 305" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)"/>
  <text x="385" y="300" font-size="10">DB Query</text>
  <path d="M 380 210 Q 500 210, 620 210" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)"/>
  <text x="490" y="205" font-size="10">MCP</text>
  <path d="M 380 310 Q 500 310, 620 310" stroke="#444" stroke-width="1.5" fill="none" marker-end="url(#arrow_ad)"/>
   <text x="490" y="325" font-size="10">MCP</text>
</svg>
```

## Technology Stack

*   **Language:** Go (Golang)
*   **Key Libraries:** Standard Go (`net/http`, etc.), `go.uber.org/zap`, `github.com/lib/pq`, internal `shared` library.

## Setup & Installation

1.  **Prerequisites:**
    *   Go (version 1.21+)
    *   Access to a running PostgreSQL database configured with the project schema (`portal/prisma/schema.prisma`).

2.  **Configuration Source:** The Gateway can load configuration from:
    *   **Database (Recommended for Production):** Reads settings, users, keys, and backend server URLs from the PostgreSQL database. Requires `GATE4AI_DATABASE_URL` environment variable.
        ```bash
        export GATE4AI_DATABASE_URL="postgresql://user:password@host:port/database?sslmode=disable"
        ```
    *   **YAML File (for Development/Testing):** Reads configuration from a YAML file. Specify path via `--config-yaml` flag or `GATE4AI_CONFIG_YAML` environment variable.
    *   **Internal (Used in Tests):** Configuration can be provided programmatically.

## Building

```bash
# From the gate4ai root directory
go build -o gateway_app ./gateway/cmd/main.go
```

## Running

*   **Using Database Config:**
    ```bash
    # Ensure GATE4AI_DATABASE_URL is set
    ./gateway_app
    # Or specify DB URL via flag
    # ./gateway_app --database-url "your_db_connection_string"
    ```
*   **Using YAML Config:**
    ```bash
    ./gateway_app --config-yaml ./path/to/your/config.yaml
    ```
    The gateway listens on the address specified in the config (`gateway_listen_address` setting or `server.address` in YAML), typically `:8080`.

*   **Docker:**
    Use `docker-compose.yml` in the root directory (recommended) or build and run the specific gateway image using `gateway/Dockerfile`. Ensure `GATE4AI_DATABASE_URL` is passed to the container.

## Configuration Details

The Gateway primarily reads its configuration from the chosen source (Database `Settings` table or YAML file). Key settings include:

*   `gateway_listen_address` / `server.address`: The address and port to listen on (e.g., `:8080`).
*   `gateway_log_level` / `server.log_level`: Logging level (`debug`, `info`, `warn`, `error`).
*   `gateway_authorization_type` / `server.authorization`: Controls MCP authorization (`users_only`, `marked_methods`, `none`).
*   `url_how_gateway_proxy_connect_to_the_portal` / `server.frontend_address`: URL of the Portal service for proxying.
*   API Key Hashes (`ApiKey` table / `users.[].keys` in YAML).
*   Backend Server Definitions (`Server` table / `backends` in YAML).

## API Endpoints

The Gateway typically exposes:

*   `/mcp`: The primary endpoint for MCP requests (V2025 and potentially V2024 via header/POST body detection).
*   `/sse`: Legacy endpoint for V2024 MCP communication (SSE GET, POST).
*   `/status`: Health check endpoint.
*   `/info` (Optional): Endpoint to get info about a target MCP server (if configured).
*   `/` (and other paths): Proxies requests to the Portal service (if `url_how_gateway_proxy_connect_to_the_portal` is set).