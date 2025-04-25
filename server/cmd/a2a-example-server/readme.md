# Gate4AI - A2A Example Server

This directory contains an example implementation of an Agent-to-Agent (A2A) protocol server. It demonstrates how to build a server that handles A2A tasks using the `gate4ai/server/a2a` package and a separate agent logic implementation (`agent/agent.go`).

## Purpose

*   **Demonstration:** Shows how to build a basic A2A-compliant server with separation between protocol handling and agent logic.
*   **Testing:** Provides a functional backend server for testing A2A clients and understanding the protocol flow.
*   **Reference:** Serves as a starting point for developing custom A2A agents and servers.

## Features Implemented

This example server implements the A2A protocol, including:

*   Serving the Agent Card at `/.well-known/agent.json`.
*   Handling `tasks/send` requests for synchronous task execution.
*   Handling `tasks/get` requests to retrieve task status and artifacts.
*   Handling `tasks/cancel` requests to cancel ongoing tasks.
*   Handling `tasks/sendSubscribe` requests for streaming updates via Server-Sent Events (SSE).
*   Handling `tasks/resubscribe` requests (basic implementation).
*   Managing task state (Submitted, Working, InputRequired, Completed, Failed, Canceled) via an in-memory store.
*   Executing agent logic based on commands parsed from user messages (see Scenarios below).
*   Context cancellation propagation to the agent handler.

## Setup & Installation

1.  **Prerequisites:**
    *   Go (version 1.24+) installed.

2.  **Configuration:**
    *   By default, the server runs with an internal configuration (no external file needed).
    *   Optionally, you can create a `config.yaml` file and provide its path using the `-config` flag to customize settings like listen address or agent card details (see `shared/config/yaml.go` for structure).

## Building

Navigate to the `gate4ai` root directory and run:

```bash
go build -o build/a2a-example-server ./server/cmd/a2a-example-server/main.go
```

This will create the executable `a2a-example-server` inside a `build` directory in the project root.

## Running

*   **Directly (Default Port 4000):**
    ```bash
    ./build/a2a-example-server
    ```
    Or specify a different port:
    ```bash
    ./build/a2a-example-server --listen :5001
    ```

*   **With YAML Configuration:**
    ```bash
    # Assuming config.yaml exists in the current directory
    ./build/a2a-example-server --config config.yaml
    # Optionally override listen address from config
    ./build/a2a-example-server --config config.yaml --listen :5002
    ```

*   **Docker:**
    Use the provided `Dockerfile` located in the parent `server` directory. Build from the **`gate4ai` root directory**:
    ```bash
    # Build the image (replace gate4ai-a2a-example-server with your desired image name)
    docker build -t gate4ai-a2a-example-server -f server/Dockerfile .

    # Run the container (maps container port 4000 to host port 4000)
    docker run -d -p 4000:4000 --name gate4ai-a2a-example gate4ai-a2a-example-server
    ```
    *Note: The default Dockerfile builds the MCP server. You might need to adjust it or create a separate one specifically for the A2A server if you need different dependencies or configurations.*

## API Endpoints

*   **A2A Protocol:** `http://localhost:4000/a2a` (Default POST endpoint for JSON-RPC calls)
*   **Agent Card:** `http://localhost:4000/.well-known/agent.json` (GET endpoint)

## Interaction Examples (`curl`)

Replace `:4000` with your actual listening port if different. Replace `"unique-task-id-..."` with a unique UUID for each new task.

**1. Get Agent Card:**

```bash
curl http://localhost:4000/.well-known/agent.json
```

**2. Scenario 1: Basic Response (Default Artifact)**

```bash
TASK_ID=$(uuidgen)
curl -X POST http://localhost:4000/a2a \
-H "Content-Type: application/json" \
-d '{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tasks/send",
  "params": {
    "id": "'$TASK_ID'",
    "message": {
      "role": "user",
      "parts": [ { "type": "text", "text": "Just a simple message." } ]
    }
  }
}'
```
*Expected Output:* Task object with state "completed" and an artifact containing "OK: Just a simple message.".

**3. Scenario 2: Explicit Text Response (No Payload)**

```bash
TASK_ID=$(uuidgen)
curl -X POST http://localhost:4000/a2a \
-H "Content-Type: application/json" \
-d '{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tasks/send",
  "params": {
    "id": "'$TASK_ID'",
    "message": {
      "role": "user",
      "parts": [ { "type": "text", "text": "Tell me something respond with text" } ]
    }
  }
}'
```
*Expected Output:* Task object with state "completed" and an artifact containing "This is the default text response.".

**4. Scenario 3: Explicit Text Response (With Payload)**

```bash
TASK_ID=$(uuidgen)
curl -X POST http://localhost:4000/a2a \
-H "Content-Type: application/json" \
-d '{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tasks/send",
  "params": {
    "id": "'$TASK_ID'",
    "message": {
      "role": "user",
      "parts": [ { "type": "text", "text": "respond with text \"{\\\"message\\\": \\\"Hello from payload!\\\"}\"" } ]
    }
  }
}'
```
*Expected Output:* Task object with state "completed" and an artifact containing the JSON string `"{\"message\": \"Hello from payload!\"}"`.

**5. Scenario 4: Delay (Get Required)**

*   **Send:**
    ```bash
    TASK_ID=$(uuidgen)
    echo "Task ID: $TASK_ID"
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 4,
      "method": "tasks/send",
      "params": {
        "id": "'$TASK_ID'",
        "message": { "role": "user", "parts": [ { "type": "text", "text": "wait 2 seconds respond with text ''Delayed Hello!''" } ] }
      }
    }'
    ```
    *Expected Immediate Output:* Task object with state "working".
*   **Get (after ~3 seconds):**
    ```bash
    # Use the TASK_ID from the previous command
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 5,
      "method": "tasks/get",
      "params": { "id": "'$TASK_ID'" }
    }'
    ```
    *Expected Output:* Task object with state "completed" and an artifact containing "Delayed Hello!".

**6. Scenario 5 & 6: Respond File (Default & Payload)**

```bash
# Default File
TASK_ID1=$(uuidgen)
curl -X POST http://localhost:4000/a2a -H "Content-Type: application/json" \
-d '{"jsonrpc":"2.0","id":6,"method":"tasks/send","params":{"id":"'$TASK_ID1'","message":{"role":"user","parts":[{"type":"text","text":"respond file"}]}}}'

# File with Payload (Base64 for "Hello World!")
TASK_ID2=$(uuidgen)
curl -X POST http://localhost:4000/a2a -H "Content-Type: application/json" \
-d '{"jsonrpc":"2.0","id":7,"method":"tasks/send","params":{"id":"'$TASK_ID2'","message":{"role":"user","parts":[{"type":"text","text":"respond file SGVsbG8gV29ybGQh"}]}}}'
```
*Expected Output:* Each request returns a completed Task with a `FilePart` artifact (default or with the specified payload).

**7. Scenario 7 & 8: Respond Data (Default & Payload)**

```bash
# Default Data
TASK_ID1=$(uuidgen)
curl -X POST http://localhost:4000/a2a -H "Content-Type: application/json" \
-d '{"jsonrpc":"2.0","id":8,"method":"tasks/send","params":{"id":"'$TASK_ID1'","message":{"role":"user","parts":[{"type":"text","text":"respond data"}]}}}'

# Data with Payload
TASK_ID2=$(uuidgen)
curl -X POST http://localhost:4000/a2a -H "Content-Type: application/json" \
-d '{"jsonrpc":"2.0","id":9,"method":"tasks/send","params":{"id":"'$TASK_ID2'","message":{"role":"user","parts":[{"type":"text","text":"respond data {\"id\": 123, \"active\": true}"}]}}}'
```
*Expected Output:* Each request returns a completed Task with a `DataPart` artifact (default or with the specified JSON payload).

**8. Scenario 9: Ask for Input**

*   **Step 1: Ask**
    ```bash
    TASK_ID=$(uuidgen)
    echo "Task ID: $TASK_ID"
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 10,
      "method": "tasks/send",
      "params": {
        "id": "'$TASK_ID'",
        "message": { "role": "user", "parts": [ { "type": "text", "text": "ask for input \"Enter city:\"" } ] }
      }
    }'
    ```
    *Expected Output:* Task object with state "input-required" and a message from the agent "Enter city:".
*   **Step 2: Respond**
    ```bash
    # Use the TASK_ID from Step 1
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 11,
      "method": "tasks/send",
      "params": {
        "id": "'$TASK_ID'",
        "message": { "role": "user", "parts": [ { "type": "text", "text": "London" } ] }
      }
    }'
    ```
    *Expected Output:* Task object with state "completed" and an artifact containing "OK: London".

**9. Scenario 10: Streaming (SSE)**

```bash
TASK_ID=$(uuidgen)
curl -N -X POST http://localhost:4000/a2a \
-H "Content-Type: application/json" \
-H "Accept: text/event-stream" \
-d '{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "tasks/sendSubscribe",
  "params": {
    "id": "'$TASK_ID'",
    "message": { "role": "user", "parts": [ { "type": "text", "text": "Generate report stream 3 chunks" } ] }
  }
}'
```
*Expected Output:* An SSE stream containing:
    1. A `task_status_update` event with state "working".
    2. Three `task_artifact_update` events with text chunks.
    3. A final `task_status_update` event with state "completed" and `final: true`.

**10. Scenario 12: Cancel Task**

*   **Start Long Task (wait 10s):**
    ```bash
    TASK_ID=$(uuidgen)
    echo "Task ID: $TASK_ID"
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 13,
      "method": "tasks/send",
      "params": { "id": "'$TASK_ID'", "message": { "role": "user", "parts": [ { "type": "text", "text": "wait 10 seconds respond text 'Too late?'" } ] } }
    }' & # Run in background
    ```
    *Expected Output (background):* Task object with state "working".
*   **Cancel (within 10 seconds):**
    ```bash
    # Use the TASK_ID from the previous command
    sleep 2 # Wait a bit before cancelling
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 14,
      "method": "tasks/cancel",
      "params": { "id": "'$TASK_ID'" }
    }'
    ```
    *Expected Output:* Task object with state "canceled".

**11. Scenario 13: Trigger Error**

```bash
# Trigger specific JSON-RPC error
TASK_ID1=$(uuidgen)
curl -X POST http://localhost:4000/a2a \
-H "Content-Type: application/json" \
-d '{ "jsonrpc":"2.0", "id":15, "method":"tasks/send", "params":{ "id":"'$TASK_ID1'", "message":{ "role":"user", "parts":[ { "type":"text", "text":"break trigger error -32601" } ] } } }'

# Trigger internal failure
TASK_ID2=$(uuidgen)
curl -X POST http://localhost:4000/a2a \
-H "Content-Type: application/json" \
-d '{ "jsonrpc":"2.0", "id":16, "method":"tasks/send", "params":{ "id":"'$TASK_ID2'", "message":{ "role":"user", "parts":[ { "type":"text", "text":"fail now trigger error fail" } ] } } }'
```
*Expected Output:* Each request returns a JSON-RPC error response (code -32601 or -32603 respectively).

**12. Scenario 14 & 15: Combined Commands (Get Required)**

*   **Send:**
    ```bash
    TASK_ID=$(uuidgen)
    echo "Task ID: $TASK_ID"
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 17,
      "method": "tasks/send",
      "params": {
        "id": "'$TASK_ID'",
        "message": { "role": "user", "parts": [ { "type": "text", "text": "wait 1 second respond with text ''First'' wait 1 second respond data {\\"id\\":1}" } ] }
      }
    }'
    ```
    *Expected Immediate Output:* Task object with state "working".
*   **Get (after ~3 seconds):**
    ```bash
    # Use the TASK_ID from the previous command
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{ "jsonrpc": "2.0", "id": 18, "method": "tasks/get", "params": { "id": "'$TASK_ID'" } }'
    ```
    *Expected Output:* Task object with state "completed" and two artifacts (one TextPart "First", one DataPart {"id":1}).

**13. Scenario 18: Input File Context**

```bash
TASK_ID=$(uuidgen)
# Base64 for "File Data Example"
FILE_CONTENT_B64="RmlsZSBEYXRhIEV4YW1wbGU="
curl -X POST http://localhost:4000/a2a \
-H "Content-Type: application/json" \
-d '{
  "jsonrpc": "2.0",
  "id": 19,
  "method": "tasks/send",
  "params": {
    "id": "'$TASK_ID'",
    "message": {
      "role": "user",
      "parts": [
        { "type": "text", "text": "Analyze this file and respond" },
        { "type": "file", "file": { "name": "context.txt", "mimeType": "text/plain", "bytes": "'$FILE_CONTENT_B64'" } }
      ]
    }
  }
}'
```
*Expected Output:* Task object with state "completed" and one artifact containing "Received file part: name='context.txt', mimeType='text/plain'".

**14. Scenario 19: Get History**

*   **Send multiple messages to the same task (e.g., using Scenario 9 steps):**
    *   Send `ask for input "Enter city:"` -> Get response (TASK_ID=abc, state=input-required)
    *   Send `London` (with `id=abc`) -> Get response (state=completed, artifact="OK: London")
*   **Get History:**
    ```bash
    # Use the TASK_ID 'abc' from the interaction above
    curl -X POST http://localhost:4000/a2a \
    -H "Content-Type: application/json" \
    -d '{
      "jsonrpc": "2.0",
      "id": 20,
      "method": "tasks/get",
      "params": { "id": "abc", "historyLength": 5 }
    }'
    ```
*Expected Output:* Task object (state=completed, artifact="OK: London") with a `history` array containing the last 5 messages (user message "ask...", agent message "Enter city:", user message "London", agent status messages/artifacts).

---
```