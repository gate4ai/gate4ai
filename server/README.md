# gate4.ai - Example MCP Server

This directory contains an example implementation of a Model Context Protocol (MCP) server using the `gate4ai/mcp/server` and `gate4ai/mcp/shared` Go libraries.

## Purpose

*   **Demonstration:** Shows how to build a basic MCP-compliant server.
*   **Testing:** Provides a functional backend server for testing the Gateway and client implementations.
*   **Reference:** Serves as a starting point for developing custom MCP servers.

## Features Implemented

This example server demonstrates:

*   Basic MCP handshake (`initialize`).
*   Handling `ping` requests.
*   Registering and handling `tools/call` requests (e.g., `echo`, `add`).
*   Registering and handling `resources/read` requests.
*   Managing resource subscriptions (`resources/subscribe`, `resources/unsubscribe`) and sending update notifications.
*   Registering and handling `prompts/get` requests.
*   (Potentially) Implementing sampling requests (`sampling/createMessage`).

## Technology Stack

*   **Language:** Go (Golang)
*   **Key Libraries:**
    *   Internal `shared` and `server` modules.
    *   `go.uber.org/zap` (Logging)

## Setup & Installation

1.  **Prerequisites:**
    *   Go (version 1.21+)

2.  **Configuration:**
    *   Uses a simple `config.yaml` file located in `server/cmd/config.yaml` for basic settings like listen address and API key definitions (for testing direct access).

## Building

```bash
# From the gate4ai root directory
go build -o example_server_app ./server/cmd/startExample.go
```

## Running

*   **Directly:**
    ```bash
    ./example_server_app --port 4001 --config ./server/cmd/mcp-example-server/config.yaml
    # Or using default config path and port 4001:
    # ./example_server_app --port 4001
    ```
    The server will listen on the specified port (or the one in the config if `--port` is omitted).

*   **Docker:**
    Use the provided `server/Dockerfile` to build a container image.
    ```bash
    # From the gate4ai root directory
    docker build -t gate4ai-example-server -f server/Dockerfile .
    docker run -d -p 4001:4001 --name gate4ai-example gate4ai-example-server
    ```

## API

The Example Server exposes the MCP endpoint, typically at `/sse` (as defined in its default configuration). Clients can connect directly using keys defined in its `config.yaml` or access it via the Gateway if registered there.