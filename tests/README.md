# gate4.ai - Testing Suite

This directory contains the integration and end-to-end (E2E) tests for the gate4.ai platform.

## Purpose

*   Verify the core functionality of the Gateway.
*   Test interactions between the Portal, Gateway, and backend MCP servers (Example Server).
*   Ensure user registration, login, API key management, server management, and subscription flows work correctly through the UI.
*   Validate MCP communication between components.

## Technology Stack

*   **Testing Framework:** Go standard testing library (`testing`)
*   **E2E Browser Automation:** [Playwright for Go](https://github.com/playwright-community/playwright-go)
*   **Assertions:** `github.com/stretchr/testify`
*   **Containerization (for dependencies):** `github.com/testcontainers/testcontainers-go` (PostgreSQL, MailHog)
*   **Logging:** `go.uber.org/zap`

## Test Structure

*   **`1. setup_test.go`:** Contains `TestMain` which sets up the entire test environment (starts database, mailhog, portal, gateway, example server) and handles cleanup.
*   **`2. artifacts_test.go`:** Defines the `ArtifactManager` for saving screenshots, HTML, and console logs during Playwright tests, aiding debugging. Includes helper functions for interacting with the UI reliably.
*   **`portal_*.go`:** Playwright tests focusing on UI interactions within the Portal (registration, login, adding servers, creating keys, etc.).
*   **`server_example_test.go`:** Basic tests directly against the Example MCP Server endpoint.
*   **`gateway_*.go`:** Tests specifically targeting the Gateway's MCP endpoint, often using different API keys to verify authorization and data aggregation.
*   **`helpers.go`:** Utility functions used across different tests.
*   **`old/`:** Contains older test implementations (may be refactored or removed).

## Running Tests

1.  **Prerequisites:**
    *   Go (version 1.21+)
    *   Node.js + npm (for Playwright browser installation)
    *   Docker (Testcontainers requires a running Docker daemon)

2.  **Install Playwright Browsers:**
    Navigate to the `tests` directory (or wherever the Playwright dependency is managed, potentially `portal`) and run:
    ```bash
    # cd tests # If needed
    npx playwright install --with-deps
    ```

3.  **Run Tests:**
    Navigate to the `tests` directory in your terminal:
    ```bash
    cd tests
    ```
    Run all tests:
    ```bash
    go test -v -timeout 90m # Use a long timeout due to setup time
    ```
    Run specific tests:
    ```bash
    go test -v -timeout 90m -run TestUserRegistration
    go test -v -timeout 90m -run TestGatewayAPIKeyAuthorization
    ```

## Artifacts

Test artifacts (screenshots, HTML, logs) are saved to the `tests/artifacts/` directory, organized by timestamp and test name. This helps in debugging failed UI tests.