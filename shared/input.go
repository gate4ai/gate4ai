package shared

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

type Input struct {
	Mu              sync.RWMutex
	input           chan *Message
	logger          *zap.Logger
	validators      []MessageValidator
	methodHandlers  sync.Map      // Maps method names to handler functions
	notFoundHandler atomic.Value  // func(*shared.Message) (interface{}, error)
	capabilities    []ICapability // List of capabilities
}

func NewInput(logger *zap.Logger) *Input {
	i := &Input{
		validators: []MessageValidator{},
		logger:     logger,
	}
	// Initialize notFoundHandler
	i.notFoundHandler.Store(func(msg *Message) (interface{}, error) {
		method := "<nil>"
		if msg.Method != nil {
			method = *msg.Method
		}
		return nil, fmt.Errorf("method not found: %s", method)
	})

	return i
}

type MessageValidator interface {
	Validate(*Message) error
}

// HandleMessage validates and enqueues a message for processing
func (i *Input) Put(msg *Message) error {
	i.Mu.Lock()
	copyOfValidators := make([]MessageValidator, len(i.validators))
	copy(copyOfValidators, i.validators)
	i.Mu.Unlock()

	for _, validator := range copyOfValidators {
		if err := validator.Validate(msg); err != nil {
			return err
		}
	}
	msg.Session.UpdateLastActivity()

	select {
	case i.input <- msg:
		i.logger.Debug("Message queued",
			zap.String("sessionID", msg.Session.GetID()),
			zap.Any("messageID", msg.ID),
			zap.Stringp("method", msg.Method),
		)

	default:
		i.logger.Error("Input channel full, dropping message",
			zap.String("sessionID", msg.Session.GetID()),
			zap.Any("messageID", msg.ID),
			zap.Stringp("method", msg.Method),
		)
		if !msg.ID.IsEmpty() {
			go msg.Session.SendResponse(msg.ID, nil, errors.New("message processor busy, message dropped"))
		}
		return errors.New("input processor busy, input channel full")
	}
	return nil
}

func (i *Input) Process() {
	i.logger.Debug("Input s- Message processing loop started.")
	i.input = make(chan *Message, 100)
	defer func() {
		close(i.input)
		i.input = nil
		i.logger.Info("Input - Message processing loop stopped.")
	}()
	for msg := range i.input {
		i.logger.Debug("Processing message",
			zap.String("sessionID", safeGetSessionID(msg.Session)),
			zap.Any("messageID", msg.ID),
			zap.Stringp("method", msg.Method),
		)
		if msg.Session == nil {
			i.logger.Error("Received message with nil session in processing queue. Must be a Client or Server session.")
			continue
		}
		logger := i.logger.With(zap.String("sessionID", msg.Session.GetID()))
		if msg.Session.GetStatus() == StatusNew &&
			(msg.Method != nil && *msg.Method != "initialize") {
			logger.Warn("Attempted to process message for a closed or new session")
		}

		if msg.Method == nil && msg.ID.IsEmpty() {
			logger.Error("Received invalid message (no method or ID)")
			continue
		}

		// Process each message in its own goroutine to prevent blocking the input channel
		go func(msgToProcess *Message) {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic recovered during message processing", zap.Any("panic", r), zap.Any("msgId", msgToProcess.ID))
					// Optionally send an internal error response back if it was a request
					if !msgToProcess.ID.IsEmpty() {
						msgToProcess.Session.SendResponse(msgToProcess.ID, nil, fmt.Errorf("internal server error during processing: %v", r))
					} else if isA2AMethod(msgToProcess.Method) {
						// A2A errors might need specific handling even without an ID,
						// e.g., if a streaming handler panics. How to report back?
						// Maybe log and close the SSE stream if applicable.
						// For now, just logging.
					}
				}
				logger.Debug("Processed message",
					zap.String("messageID", msgToProcess.ID.String()),
					zap.String("method", NilIfNil(msgToProcess.Method)),
				)
			}() // End defer for panic recovery and logging
			if msgToProcess.Method != nil {
				if handler, exists := i.GetHandler(*msgToProcess.Method); exists {
					response, err := handler(msg) // Execute the handler

					// Only send a response if the original message had an ID (i.e., it was a request) and wasn't a notification method
					if !msgToProcess.ID.IsEmpty() && !isNotificationMethod(msgToProcess.Method) {
						msgToProcess.Session.SendResponse(msgToProcess.ID, response, err)
					} else if err != nil {
						if !isA2AMethod(msgToProcess.Method) {
							// Log errors from MCP notification handlers
							logger.Error("Error handling notification", zap.String("method", *msgToProcess.Method), zap.Error(err))
						}
						// Errors from A2A methods (especially streaming) might be handled by sending error events over SSE.
					}
				} else {
					// Handler not found
					errMsg := fmt.Errorf("handler not found for method: %s", NilIfNil(msgToProcess.Method))
					logger.Error(errMsg.Error())
					if !msgToProcess.ID.IsEmpty() {
						msgToProcess.Session.SendResponse(msgToProcess.ID, nil, &JSONRPCError{Code: JSONRPCErrorMethodNotFound, Message: fmt.Sprintf("Method not found: %s", *msgToProcess.Method)})
					}
				}
			} else if !msgToProcess.ID.IsEmpty() {
				// Handle responses to server-initiated requests
				processed := msg.Session.GetRequestManager().ProcessResponse(msg)
				if !processed {
					logger.Warn("Received response for unknown or timed-out request",
						zap.String("responseID", msgToProcess.ID.String()),
					)
				}
			}
		}(msg) // Pass msg to goroutine
	}
}

func isNotificationMethod(method *string) bool {
	return method != nil && strings.HasPrefix(*method, "notifications/")
}

func isA2AMethod(method *string) bool {
	return method != nil && strings.HasPrefix(*method, "tasks/")
}

// AddNotFoundHandle registers a handler for methods that don't have a specific handler
func (i *Input) AddNotFoundHandle(handler func(*Message) (interface{}, error)) {
	i.notFoundHandler.Store(handler)
	i.logger.Debug("Registered not-found handler")
}

// GetHandler retrieves a handler for a specific method
func (i *Input) GetHandler(method string) (func(*Message) (interface{}, error), bool) {
	handler, exists := i.methodHandlers.Load(method)
	if !exists {
		// Use the stored notFoundHandler
		notFoundFunc := i.notFoundHandler.Load()
		if notFoundFuncTyped, ok := notFoundFunc.(func(*Message) (interface{}, error)); ok {
			return notFoundFuncTyped, true // Indicate a handler (the notFound one) was found
		}
		// Fallback if notFoundHandler wasn't set correctly (shouldn't happen with NewManager)
		return nil, false
	}
	return handler.(func(*Message) (interface{}, error)), true
}

// AddValidator adds custom message validators
func (i *Input) AddValidator(validators ...MessageValidator) {
	i.Mu.Lock()
	defer i.Mu.Unlock()
	i.validators = append(i.validators, validators...)
}

// This method avoids the addition of incorrect capabilities (static analyzer assistance).
func (i *Input) AddServerCapability(capabilities ...IServerCapability) {
	for _, capability := range capabilities {
		i.addCapability(capability.(ICapability))
	}
}

// This method avoids the addition of incorrect capabilities (static analyzer assistance).
func (i *Input) AddClientCapability(capabilities ...IClientCapability) {
	for _, capability := range capabilities {
		i.addCapability(capability.(ICapability))
	}
}

func (i *Input) addCapability(capability ICapability) {
	i.capabilities = append(i.capabilities, capability)
	for method, handler := range capability.GetHandlers() {
		i.methodHandlers.Store(method, handler)
		i.logger.Debug("Registered handler from capability",
			zap.String("capability", fmt.Sprintf("%T", capability)),
			zap.String("method", method))
	}
}

func (i *Input) SetCapabilities(clientOrServerCapabilities any) {
	// All capabilities must implement the same IServerCapability or IClientCapability interface
	if clientCapabilities, ok := clientOrServerCapabilities.(*schema.ClientCapabilities); ok {
		for _, capability := range i.capabilities {
			if clientCapability, ok := capability.(IClientCapability); ok {
				clientCapability.SetCapabilities(clientCapabilities)
			} else {
				i.logger.Error("Capability does not implement IClientCapability",
					zap.String("capability", fmt.Sprintf("%T", capability)))
			}
		}
	} else if serverCapabilities, ok := clientOrServerCapabilities.(*schema.ServerCapabilities); ok {
		for _, capability := range i.capabilities {
			if serverCapability, ok := capability.(IServerCapability); ok {
				serverCapability.SetCapabilities(serverCapabilities)
			} else {
				i.logger.Error("Capability does not implement IServerCapability",
					zap.String("capability", fmt.Sprintf("%T", capability)))
			}
		}
	} else {
		i.logger.Error("clientOrServerCapabilities must be a *ClientCapabilities or *ServerCapabilities",
			zap.String("argument", fmt.Sprintf("%T", clientOrServerCapabilities)))
	}
}

// safeGetSessionID returns the session ID or "nil" if the session is nil
func safeGetSessionID(session ISession) string {
	if session == nil {
		return "sessionIsNil"
	}
	return session.GetID()
}
