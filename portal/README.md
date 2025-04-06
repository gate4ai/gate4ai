# gate4.ai - Portal

This directory contains the frontend web application for the gate4.ai platform, built with Nuxt.js 3.

## Purpose

The Portal provides a user-friendly interface for:

*   Managing MCP Servers (adding, editing, viewing).
*   Discovering available servers in the catalog.
*   Managing user accounts and permissions (Admin/Security roles).
*   Managing personal API keys.
*   Subscribing/unsubscribing to servers.
*   Viewing server details, including available tools and parameters.
*   Managing application-wide settings (Admin role).

## Technology Stack

*   **Framework:** [Nuxt.js 3](https://nuxt.com/)
*   **UI Library:** [Vuetify 3](https://vuetifyjs.com/)
*   **Language:** TypeScript
*   **Database ORM:** [Prisma](https://www.prisma.io/)
*   **Styling:** Sass, Material Design Icons
*   **Linting:** ESLint

## Setup & Installation

1.  **Prerequisites:**
    *   Node.js (LTS version recommended)
    *   npm
    *   Access to a running PostgreSQL database.

2.  **Environment Variables:**
    *   `GATE4AI_DATABASE_URL`: **Required.** The connection string for your PostgreSQL database.
        ```bash
        export GATE4AI_DATABASE_URL="postgresql://user:password@host:port/database?sslmode=disable"
        ```
    *   `JWT_SECRET`: **Required.** A secret key used for signing JWT tokens for user authentication. Generate a strong, random secret.
        ```bash
        # Example generation:
        # node -e "console.log(require('crypto').randomBytes(32).toString('hex'))"
        export JWT_SECRET="your_super_secret_key_here"
        ```
    *   `API_BASE_URL` (Public Runtime Config): Optional. Base path for API calls made from the frontend. Defaults to `/api`.
        ```bash
        # Example (if API is hosted elsewhere):
        # export NUXT_PUBLIC_API_BASE_URL="https://api.example.com"
        ```

3.  **Install Dependencies:**
    ```bash
    npm install
    ```

4.  **Database Setup:**
    *   Apply database migrations:
        ```bash
        npx prisma migrate dev --name init
        ```
    *   Generate Prisma Client:
        ```bash
        npx prisma generate
        ```
    *   Seed the database (creates admin user, default settings):
        ```bash
        npx prisma db seed
        ```

## Running the Portal

*   **Development Mode (with Hot Reloading):**
    ```bash
    npm run dev
    ```
    Access at `http://localhost:3000` (or the configured port).

*   **Build for Production:**
    ```bash
    npm run build
    ```
    This creates the `.output` directory with the optimized production build.

*   **Preview Production Build:**
    ```bash
    npm run preview
    ```
    Starts a server serving the production build (useful for testing before deployment).

*   **Running Production Build Directly:**
    After building, you can run the server using Node.js:
    ```bash
    node .output/server/index.mjs
    ```
    Ensure `GATE4AI_DATABASE_URL` and `JWT_SECRET` environment variables are set in the production environment.

## Linting

To check for code style issues:
```bash
npm run lint
```

## Deployment

*   **Node.js Server:** Build the application (`npm run build`) and run the output server (`node .output/server/index.mjs`) using a process manager like PM2 or systemd. Ensure environment variables are set.
*   **Docker:** Use the provided `Dockerfile` to containerize the application. See the root `README.md` and `docker-compose.yml` for container orchestration examples.
*   **Serverless/Static:** While possible to generate a static site (`nuxt generate`), this application relies heavily on its backend API routes and database interaction, making a Node.js server or Docker deployment more suitable.