package mcpClient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gate4ai/mcp/gateway/clients/mcpClient/capability"
	"github.com/gate4ai/mcp/shared"
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"github.com/r3labs/sse/v2"
	"go.uber.org/zap"
	"gopkg.in/cenkalti/backoff.v1"
)

// Ensure Session implements ISession
var _ shared.ISession = (*Session)(nil)

// Session represents a connection to a single backend MCP server.
type Session struct {
	*shared.BaseSession                                                  // Embed BaseSession for common fields (ID, Logger, Status, Output, etc.)
	Locker                       sync.RWMutex                            // Separate locker for client-specific fields if needed (or rely on BaseSession.Mu)
	Backend                      *Backend                                // Information about the backend server
	postEndpoint                 string                                  // POST endpoint URL received from backend
	ctx                          context.Context                         // Context for managing the session lifecycle
	sseClient                    *sse.Client                             // SSE client instance
	httpClient                   *http.Client                            // HTTP client for POST requests
	sseCh                        chan *sse.Event                         // Channel for receiving SSE events
	closeCh                      chan struct{}                           // Channel to signal explicit session closure
	initialization               chan error                              // Channel to signal completion/failure of initialization handshake
	serverInfo                   *schema.Implementation                  // Backend server info (V2025 type)
	tools                        []schema.Tool                           // Cached tools list (V2025 type)
	toolsInitialized             bool                                    // Flag indicating if tools have been fetched
	prompts                      []schema.Prompt                         // Cached prompts list (V2025 type)
	promptsInitialized           bool                                    // Flag indicating if prompts have been fetched
	resources                    []schema.Resource                       // Cached resources list (V2025 type)
	resourcesInitialized         bool                                    // Flag indicating if resources have been fetched
	resourceTemplates            []schema.ResourceTemplate               // Cached resource templates list (V2025 type)
	resourceTemplatesInitialized bool                                    // Flag indicating if resource templates have been fetched
	inputProcessor               *shared.Input                           // Input processor for this session
	SamplingCapability           *capability.SamplingCapability          // Sampling capability instance
	ResourcesCapability          *capability.ResourcesCapability         // Resources capability instance
	ResourceTemplatesCapability  *capability.ResourceTemplatesCapability // Resource templates capability instance
}

// writeInitializationErrorAndClose safely writes to the initialization channel and closes it.
// It prevents double closes and handles existing errors.
func (s *Session) writeInitializationErrorAndClose(newErr error) {
	s.Locker.Lock() // Use client Locker to protect initialization channel access
	defer s.Locker.Unlock()

	// Check if the initialization channel exists and is not already closed
	if s.initialization == nil {
		if newErr != nil {
			// Log the error even if the channel is already gone (e.g., subsequent errors after init)
			s.BaseSession.Logger.Error("Initialization channel is nil, cannot signal error", zap.Error(newErr))
		}
		return
	}

	// Attempt to read to see if it's closed or has a value
	select {
	case oldErr, ok := <-s.initialization:
		if !ok {
			// Channel was already closed. Log the new error if it exists.
			if newErr != nil {
				s.BaseSession.Logger.Error("Initialization channel already closed, encountered new error", zap.Error(newErr))
			}
			// Ensure initialization is set to nil after confirming it's closed
		} else {
			// Channel was open and contained an error (or nil).
			s.BaseSession.Logger.Warn("Initialization channel already had value, combining errors", zap.Error(oldErr), zap.Error(newErr))
			s.initialization <- errors.Join(oldErr, newErr)
			close(s.initialization)
		}
	default:
		// Channel was open and empty. Send the new error (if any) and close.
		if newErr != nil {
			s.initialization <- newErr
			s.BaseSession.Logger.Error("Signaling initialization failure", zap.Error(newErr))
		} else {
			s.BaseSession.Logger.Info("Signaling initialization success")
		}
		close(s.initialization)
	}
}

// Open initiates the connection and handshake process with the backend server.
// It returns a channel that signals the completion (nil error) or failure (non-nil error)
// of the initialization process. Subsequent calls return the same channel.
func (s *Session) Open() chan error {
	logger := s.BaseSession.Logger // Logger already has backend and session context
	logger.Debug("Open() called")

	s.Locker.Lock() // Use client Locker

	// 1. Check if initialization is already complete (connected)
	if s.GetStatus() == shared.StatusConnected {
		logger.Debug("Session already connected")
		// If initialization channel exists (from previous successful open), return it.
		if s.initialization != nil {
			s.Locker.Unlock()
			return s.initialization
		}
		// Otherwise, create and return a new, already closed channel signaling success.
		closedChan := make(chan error)
		close(closedChan)
		s.initialization = closedChan // Store it
		s.Locker.Unlock()
		return closedChan
	}

	// 2. Check if initialization is currently in progress
	if s.initialization != nil {
		logger.Debug("Initialization already in progress, returning existing channel")
		s.Locker.Unlock()
		return s.initialization
	}

	// 3. Start new initialization process
	logger.Info("Starting new session initialization")
	s.initialization = make(chan error, 1) // Buffered channel to prevent blocking write
	s.SetStatus(shared.StatusConnecting)
	// Ensure closeCh is (re)created if needed
	if s.closeCh == nil {
		s.closeCh = make(chan struct{})
	}
	// Reset internal state related to connection (e.g., postEndpoint)
	s.postEndpoint = ""
	s.serverInfo = nil

	s.Locker.Unlock() // Unlock before potentially blocking operations

	// Subscribe to SSE events
	logger.Debug("Subscribing to SSE channel")
	sseContext, sseCancel := context.WithCancel(s.ctx)
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.MaxElapsedTime = 60 * time.Second // Retry indefinitely until context is cancelled
	s.sseClient.ReconnectStrategy = backoff.WithContext(expBackoff, sseContext)
	s.sseClient.ReconnectNotify = func(err error, t time.Duration) {
		logger.Error("SSE connection error", zap.Error(err), zap.Duration("delay", t))
		if err.Error() == "could not connect to stream: Unauthorized" {
			sseCancel()
		}
	}
	err := s.sseClient.SubscribeChanWithContext(sseContext, "", s.sseCh) // Pass the context
	if err != nil {
		logger.Warn("Failed to subscribe to SSE events", zap.Error(err))
		s.SetStatus(shared.StatusNew)
		s.writeInitializationErrorAndClose(fmt.Errorf("SSE subscription failed: %w", err))
		return s.initialization
	}
	logger.Debug("SSE subscription initiated")

	if s.Input() == nil {
		logger.Error("Input is nil, cannot process messages")
		s.writeInitializationErrorAndClose(errors.New("input is nil, cannot process messages"))
		return s.initialization
	}

	go s.processLoop()

	return s.initialization
}

// processLoop is the main event loop for the session.
func (s *Session) processLoop() {
	loopLogger := s.BaseSession.Logger.With(zap.String("goroutine", "processLoop"))
	loopLogger.Debug("Starting session processing loop")

	defer func() {
		loopLogger.Info("Session processing loop ended")
		// Cleanup resources when loop exits
		s.Locker.Lock()
		if s.sseClient != nil {
			s.sseClient.Unsubscribe(s.sseCh) // Unsubscribe from SSE channel
		}
		s.Locker.Unlock()

		// Set status back to New
		s.SetStatus(shared.StatusNew)

		// Ensure initialization channel is closed with an error if the loop exits unexpectedly
		// before initialization completes successfully or fails explicitly.
		s.writeInitializationErrorAndClose(errors.New("session processing loop exited unexpectedly"))
	}()

	output, ok := s.AcquireOutput()
	if !ok {
		loopLogger.Error("Failed to acquire output channel")
		return
	}
	defer s.ReleaseOutput()

	for {
		select {
		case sendMsg, ok := <-output:
			if !ok {
				loopLogger.Info("Output channel closed, exiting loop")
				return
			}
			if sendMsg != nil {
				s.executeSendRequest(sendMsg)
			} else {
				loopLogger.Warn("Received nil message from Output channel")
			}

		case event, ok := <-s.sseCh:
			if !ok {
				loopLogger.Info("SSE channel closed, exiting loop")
				return // Exit loop if SSE channel is closed
			}
			if event == nil {
				loopLogger.Warn("Received nil event from SSE channel, skipping")
				continue
			}

			loopLogger.Debug("Received SSE event", zap.String("eventID", string(event.ID)), zap.String("eventName", string(event.Event)), zap.ByteString("data", event.Data))

			switch string(event.Event) {
			case "endpoint":
				s.Locker.Lock()
				endpointSet := s.postEndpoint != ""
				s.Locker.Unlock()
				// Only process the first endpoint event after connection/reconnection
				if !endpointSet {
					if len(event.Data) == 0 {
						loopLogger.Error("Received endpoint event with empty data")
						s.writeInitializationErrorAndClose(errors.New("protocol error: received empty endpoint data"))
						return // Fatal error for initialization
					}
					postURLStr := string(event.Data)
					postURL, err := url.Parse(postURLStr)
					if err != nil {
						loopLogger.Error("Failed to parse postUrl from endpoint event", zap.Error(err), zap.String("data", postURLStr))
						s.writeInitializationErrorAndClose(fmt.Errorf("invalid endpoint URL '%s': %w", postURLStr, err))
						return // Fatal error for initialization
					}

					s.Locker.Lock()
					// Resolve potentially relative endpoint URL against the base backend URL
					s.postEndpoint = s.Backend.URL.ResolveReference(postURL).String()
					s.Locker.Unlock()
					loopLogger.Info("Received POST endpoint", zap.String("endpoint", s.postEndpoint))

					// Now that we have the endpoint, initiate the MCP initialize handshake
					// Use a background context for initialize as the main loop needs to continue
					go s.sendInitialize()
				} else {
					loopLogger.Debug("Ignoring subsequent endpoint event")
				}
				continue // Continue loop after processing endpoint event
			case "message":
				if len(event.Data) == 0 {
					loopLogger.Warn("Received message event with empty data, skipping")
					continue
				}

				// Parse the JSON-RPC message
				msgs, err := shared.ParseMessages(s, event.Data)
				if err != nil {
					loopLogger.Error("Failed to parse JSON-RPC message from SSE", zap.Error(err), zap.ByteString("data", event.Data))
					continue // Don't stop the loop for one bad message
				}

				// Process the message (route to request manager or notification handlers)
				for _, msg := range msgs {
					s.Input().Put(msg)
				}
			case "ping":
				loopLogger.Debug("Received ping event")
			default:
				loopLogger.Warn("Received unknown SSE event type", zap.String("eventName", string(event.Event)))
			}

		// --- Handle explicit close signal ---
		case <-s.closeCh:
			loopLogger.Info("Session explicitly closed via closeCh")
			// writeInitializationErrorAndClose will be called by the defer.
			return // Exit loop

		// --- Handle context cancellation ---
		case <-s.ctx.Done():
			loopLogger.Info("Session context cancelled", zap.Error(s.ctx.Err()))
			// writeInitializationErrorAndClose will be called by the defer.
			return // Exit loop
		}
	}
}

// Close signals the session to terminate its connection and processing loop.
// It's safe to call multiple times.
func (s *Session) Close() error {
	logger := s.BaseSession.Logger
	logger.Info("Close() called, initiating session closure")

	// 1. Check if already closed/closing (idempotency)
	s.Locker.Lock()
	// Check closeCh first
	if s.closeCh == nil {
		s.Locker.Unlock()
		logger.Debug("Session already closed or closing (closeCh is nil)")
		return nil
	}

	// 2. Signal the processing loop to stop via closeCh
	// Ensure closeCh is closed only once
	select {
	case <-s.closeCh:
		// Already closed
		logger.Debug("closeCh already closed")
	default:
		close(s.closeCh)
		s.closeCh = nil // Set to nil after closing to prevent reuse/double close
		logger.Debug("Signaled processing loop to stop via closeCh")
	}
	s.Locker.Unlock() // Unlock after managing closeCh

	// 3. Set status immediately (though loop will also set it on exit)
	s.SetStatus(shared.StatusNew)

	// 4. Close the BaseSession (which closes the Output channel)
	// This will cause the loop to exit if it was blocked on reading Output.
	// Call BaseSession.Close() which handles closing the Output channel safely.
	baseErr := s.BaseSession.Close()
	if baseErr != nil {
		logger.Error("Error during BaseSession.Close (closing Output channel)", zap.Error(baseErr))
		// Continue with other cleanup steps
	} else {
		logger.Debug("BaseSession Output channel closed")
	}

	// 5. Unsubscribe SSE client (important to stop potential reconnections)
	// This might already be handled by context cancellation in SubscribeChanWithContext,
	// but explicit unsubscribe here provides robustness.
	s.Locker.Lock()
	if s.sseClient != nil {
		// sseClient.Unsubscribe() might block or panic if called concurrently,
		// but since the loop is signaled to stop, this should be relatively safe.
		// Alternatively, rely solely on context cancellation passed to SubscribeChanWithContext.
		// Let's keep explicit unsubscribe for clarity, assuming library handles it safely.
		s.sseClient.Unsubscribe(s.sseCh) // Unsubscribe from the specific channel
		logger.Debug("Unsubscribed from SSE client channel")
	}
	s.Locker.Unlock()

	logger.Info("Session close process completed")
	return baseErr // Return error from BaseSession.Close if any occurred
}
