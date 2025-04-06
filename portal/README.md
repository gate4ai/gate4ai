# gate4.ai - Portal

This directory contains the frontend web application and its associated backend API for the gate4.ai platform, built with Nuxt.js 3.

## Purpose

The Portal provides the user interface and core management API, typically accessed via the main Gateway's proxy. Its responsibilities include:

*   **User Interface:** Provides a web-based UI for managing the platform.
*   **Backend API (`/api/...`):** Exposes RESTful endpoints for:
    *   User authentication (login, registration) and profile management.
    *   API Key creation and deletion for the logged-in user.
    *   Server catalog management (adding, editing, viewing, deleting servers).
    *   Subscription management (creating, deleting, updating subscription status by owners/admins).
    *   User management (listing, viewing, editing roles/status - Admin/Security only).
    *   Settings management (viewing/editing application settings - Admin only).
*   **Database Interaction:** Uses Prisma ORM to interact with the PostgreSQL database for storing and retrieving all management data (users, servers, keys, subscriptions, settings, etc.).

## Technology Stack

*   **Framework:** [Nuxt.js 3](https://nuxt.com/) (Vue.js 3)
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
    *   `GATE4AI_DATABASE_URL`: **Required.** PostgreSQL connection string.
        ```bash
        export GATE4AI_DATABASE_URL="postgresql://user:password@host:port/database?sslmode=disable"
        ```
    *   `JWT_SECRET`: **Required.** Secret key for signing JWT authentication tokens.
        ```bash
        export JWT_SECRET="your_super_secret_key_here"
        ```
    *   `API_BASE_URL` (Public Runtime Config): Optional. Base path for API calls made from the *frontend* part of the portal *to its own backend*. Defaults to `/api`. Usually doesn't need changing unless deploying frontend and backend separately.
    *   `PORT` (Optional): Port for the Nuxt server to listen on. Defaults to `3000`.

3.  **Install Dependencies:**
    ```bash
    npm install
    ```

4.  **Database Setup:**
    *   Apply migrations: `npx prisma migrate dev --name init`
    *   Generate Prisma Client: `npx prisma generate`
    *   Seed database: `npx prisma db seed` (Creates admin user, default settings)

## Running the Portal

*   **Development Mode:**
    ```bash
    npm run dev
    ```
    Access directly at `http://localhost:3000`. API calls will go to the Nuxt dev server's `/api` routes.

*   **Build for Production:**
    ```bash
    npm run build
    ```

*   **Running Production Build:**
    ```bash
    node .output/server/index.mjs
    ```
    Ensure `GATE4AI_DATABASE_URL` and `JWT_SECRET` are set. In a typical deployment (like `docker-compose`), this server runs internally and is accessed via the main Gateway's proxy.

## Deployment

*   **Docker Compose (Recommended):** Use the `docker-compose.yml` in the project root. The Portal service runs internally, and the Gateway service proxies requests to it.
*   **Standalone Node.js Server:** Build (`npm run build`) and run (`node .output/server/index.mjs`) behind a reverse proxy (like Nginx or the gate4ai Gateway). Ensure environment variables are set.