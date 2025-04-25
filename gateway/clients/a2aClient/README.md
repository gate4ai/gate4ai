# A2A Client (Go)

This directory contains a Go client implementation for the Agent-to-Agent (A2A) communication protocol.

## Purpose

This client provides Go applications with the ability to interact with A2A-compliant servers. It handles:

*   Discovering agent capabilities via `/.well-known/agent.json`.
*   Sending tasks (`tasks/send`).
*   Retrieving task status (`tasks/get`).
*   Requesting task cancellation (`tasks/cancel`).
*   Subscribing to real-time task updates via Server-Sent Events (SSE) (`tasks/sendSubscribe`).
*   Resubscribing to task updates (`tasks/resubscribe`).
*   Managing push notification configurations (`tasks/pushNotification/set`, `tasks/pushNotification/get`).

## Key Components

*   **`client.go`:** Defines the main `Client` struct and methods for all A2A RPC calls. Handles synchronous requests and initiates SSE streams.
*   **`agent.go`:** Contains the `AgentInfo` struct (representing the `AgentCard`) and the `FetchAgentCard` function for discovery.

## Basic Usage

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	a2a "github.com/gate4ai/gate4ai/gateway/clients/a2aClient"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewDevelopment()
	client, err := a2a.New("http://localhost:41241", logger, nil) // Replace with your A2A server URL
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Discover agent info
	agentInfo, err := client.FetchAgentInfo(ctx)
	if err != nil {
		log.Fatalf("Failed to fetch agent info: %v", err)
	}
	fmt.Printf("Connected to Agent: %s (v%s)\n", agentInfo.Name, agentInfo.Version)

	// Send a simple task
	taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
	sendParams := a2aSchema.TaskSendParams{
		ID: taskID,
		Message: a2aSchema.Message{
			Role:  "user",
			Parts: []a2aSchema.Part{a2aSchema.Part(`{"type":"text", "text":"Hello A2A agent!"}`)},
		},
	}

	finalTask, err := client.SendTask(ctx, sendParams)
	if err != nil {
		log.Fatalf("SendTask failed: %v", err)
	}
	fmt.Printf("SendTask completed. Final Status: %s\n", finalTask.Status.State)
	if finalTask.Status.Message != nil && len(finalTask.Status.Message.Parts) > 0 {
         // Example of accessing text part (add type checking for safety)
        var textPart a2aSchema.TextPart
        if json.Unmarshal(finalTask.Status.Message.Parts[0], &textPart) == nil && textPart.Type == "text" {
            fmt.Printf("Agent Response: %s\n", textPart.Text)
        }
	}
}
```

## Streaming Usage

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	a2a "github.com/gate4ai/gate4ai/gateway/clients/a2aClient"
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewDevelopment()
	client, err := a2a.New("http://localhost:41241", logger, nil) // Replace with your A2A server URL
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // Longer timeout for streaming
	defer cancel()

	taskID := fmt.Sprintf("stream-task-%d", time.Now().UnixNano())
	subscribeParams := a2aSchema.TaskSendParams{
		ID: taskID,
		Message: a2aSchema.Message{
			Role:  "user",
			Parts: []a2aSchema.Part{a2aSchema.Part(`{"type":"text", "text":"Generate a Go function and stream the result."}`)},
		},
	}

	eventChan, err := client.SendTaskSubscribe(ctx, subscribeParams)
	if err != nil {
		log.Fatalf("SendTaskSubscribe failed: %v", err)
	}

	fmt.Println("Subscribed to task updates...")
	for event := range eventChan { // Loop until channel is closed
		if event.Error != nil {
			log.Printf("Error received from stream: %v\n", event.Error)
			break
		}

		if event.Status != nil {
			fmt.Printf("  [STATUS] State: %s, Final: %t, Message: %s\n",
				event.Status.Status.State,
				event.Final,
				event.Status.Status.Message) // Message might be nil
			if event.Final {
				fmt.Println("Stream finished.")
				// Loop will terminate as channel will be closed after this
			}
		} else if event.Artifact != nil {
			artifactName := "unnamed"
			if event.Artifact.Artifact.Name != nil {
				artifactName = *event.Artifact.Artifact.Name
			}
			fmt.Printf("  [ARTIFACT] Index: %d, Name: %s, Parts: %d\n",
				event.Artifact.Artifact.Index,
				artifactName,
				len(event.Artifact.Artifact.Parts))
			// Process artifact parts as needed
		}
	}
	fmt.Println("Exited event loop.")
} 