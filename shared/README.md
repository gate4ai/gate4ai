# gate4.ai - Shared Library

This directory contains shared Go code and data structures used by other components within the gate4.ai project, primarily the `gateway` and `server` modules.

## Purpose

*   **Common Code:** Provides reusable utilities, interfaces, and base implementations.
*   **MCP Definitions:** Contains Go structures representing the Model Context Protocol (MCP) versions (currently 2024 and 2025 schemas).
*   **Configuration Interfaces:** Defines interfaces (`IConfig`, etc.) for configuration management, allowing different implementations (YAML, Database).
*   **Session Management:** Includes base session logic (`BaseSession`) used by both client and server implementations.
*   **JSON-RPC Structures:** Defines standard JSON-RPC request, response, and error formats.

## Key Components

*   **`config/`:** Interfaces and implementations for handling application configuration (YAML, Database, In-Memory).
*   **`mcp/`:** Go structs representing different versions of the MCP schema (e.g., `2024/schema/`, `2025/schema/`).
*   **`capability.go`:** Interfaces for client/server capabilities.
*   **`input.go`:** Input message processing logic.
*   **`jsonrpc.go`:** Standard JSON-RPC structures.
*   **`message.go`:** Definition of the internal `Message` struct used for communication.
*   **`requestManager.go`:** Handles tracking requests and their callbacks.
*   **`session.go`:** Base session implementation (`BaseSession`) and the `ISession` interface.

## Usage

Other Go modules within the project (`gateway`, `server`, `tests`) import this `shared` module using Go's workspace or replace directives in their `go.mod` files.

Example `go.mod` replace directive:

```go
replace github.com/gate4ai/mcp/shared => ../shared
```

This ensures that the local version of the shared code is used during development and testing.