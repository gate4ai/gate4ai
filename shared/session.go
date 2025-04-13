package shared

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// SessionStatus represents the current state of a session
type SessionStatus int

const (
	StatusNew SessionStatus = iota
	StatusConnecting
	StatusConnected
)

type ISession interface {
	GetID() string

	AcquireOutput() (<-chan *Message, bool)
	ReleaseOutput()
	Input() *Input

	SendResponse(msgId *schema.RequestID, result interface{}, err error)
	SendNotification(method string, params map[string]any)
	SendRequest(method string, params interface{}, callback RequestCallback) (*schema.RequestID, error)
	SendA2AStreamEvent(event *A2AStreamEvent) error
	SendRequestSync(method string, params interface{}) <-chan *Message

	SetNegotiatedVersion(version string)
	GetNegotiatedVersion() string

	GetLastActivity() time.Time
	UpdateLastActivity()

	GetStatus() SessionStatus
	SetStatus(status SessionStatus)
	Close() error
	GetRequestManager() *RequestManager
	NextMessageID() schema.RequestID
	GetParamsMutex() *sync.RWMutex
	GetParams() *sync.Map
	GetLogger() *zap.Logger
}

var _ ISession = (*BaseSession)(nil)

// BaseSession provides common session fields and functionality for both client and server implementations
type BaseSession struct {
	Mu                sync.RWMutex
	ID                string
	messageID         uint64
	CreatedAt         time.Time
	LastActivity      atomic.Value
	status            SessionStatus
	ParamsMutex       sync.RWMutex
	Params            *sync.Map
	RequestManager    *RequestManager
	output            chan *Message
	isOutputAcquired  bool
	Logger            *zap.Logger
	negotiatedVersion string
	inputProcessor    *Input
}

// NewBaseSession creates a new base session with default values
func NewBaseSession(logger *zap.Logger, inputProcessor *Input, params *sync.Map) *BaseSession {
	if params == nil {
		params = &sync.Map{}
	}
	sessionID := RandomID()
	sessionLogger := logger.With(zap.String("session_id", sessionID))
	sessionLogger.Debug("Creating new session")
	s := &BaseSession{
		Logger:         sessionLogger,
		ID:             sessionID,
		CreatedAt:      time.Now(),
		status:         StatusNew,
		Params:         params,
		RequestManager: NewRequestManager(sessionLogger),
		Mu:             sync.RWMutex{},
		ParamsMutex:    sync.RWMutex{},
		output:         make(chan *Message, 100), // TODO: Make configurable
		inputProcessor: inputProcessor,
	}
	s.UpdateLastActivity()
	return s
}

func RandomID() string {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	if err != nil {
		panic(err)
	}
	return base64.URLEncoding.EncodeToString(key)
}

func (s *BaseSession) NextMessageID() schema.RequestID {
	return schema.RequestID_FromUInt64(atomic.AddUint64(&s.messageID, 1))
}

// GetID returns the unique session identifier
func (s *BaseSession) GetID() string {
	return s.ID
}

func (s *BaseSession) GetParams() *sync.Map {
	return s.Params
}

func (s *BaseSession) GetParamsMutex() *sync.RWMutex {
	return &s.ParamsMutex
}

// GetStatus returns the current status of the session
func (s *BaseSession) GetStatus() SessionStatus {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.status
}

// SetStatus updates the status of the session
func (s *BaseSession) SetStatus(status SessionStatus) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.status = status
}

// UpdateActivity updates the last activity timestamp for the session
func (s *BaseSession) UpdateLastActivity() {
	s.LastActivity.Store(time.Now())
}

func (s *BaseSession) GetLastActivity() time.Time {
	return s.LastActivity.Load().(time.Time)
}

// GetRequestManager returns the request manager for this session
func (s *BaseSession) GetRequestManager() *RequestManager {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.RequestManager
}

func (s *BaseSession) Close() error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.status = StatusNew
	if s.output == nil {
		s.Logger.Error("Double close of session")
		return nil
	}
	close(s.output)
	s.isOutputAcquired = false
	s.output = nil // TODO: need the open function in interface?
	return nil
}

func (s *BaseSession) AcquireOutput() (<-chan *Message, bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	if s.isOutputAcquired || s.output == nil {
		s.Logger.Debug("Output channel is not available",
			zap.Bool("outputAcquired", s.isOutputAcquired),
			zap.Bool("outputIsNil", s.output == nil),
		)
		return nil, false
	}
	s.isOutputAcquired = true
	return s.output, true
}

func (s *BaseSession) ReleaseOutput() {
	s.isOutputAcquired = false
}

// SetNegotiatedVersion stores the protocol version agreed upon during initialization.
func (s *BaseSession) SetNegotiatedVersion(version string) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.negotiatedVersion = version
}

// GetNegotiatedVersion retrieves the negotiated protocol version for the session.
func (s *BaseSession) GetNegotiatedVersion() string {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.negotiatedVersion
}

// SendNotification sends a notification (a message without an ID) to the output channel
func (s *BaseSession) SendNotification(method string, params map[string]any) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	var jsonParams *json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			s.Logger.Error("failed to marshal notification params", zap.Error(err))
			return
		}
		raw := json.RawMessage(data)
		jsonParams = &raw
	}
	s.UpdateLastActivity()
	s.output <- &Message{
		Session:   s,
		Timestamp: time.Now(),
		Method:    &method,
		Params:    jsonParams,
	}
}

// SendRequest sends a request and waits for a response
func (s *BaseSession) SendRequest(method string, params interface{}, callback RequestCallback) (*schema.RequestID, error) {
	if s.GetStatus() != StatusConnected && method != "initialize" {
		s.Logger.Warn("Request sent to not connected session",
			zap.String("method", method),
			zap.Any("params", params),
		)
	}

	msgID := s.NextMessageID()
	var jsonParams *json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request parameters: %w", err)
		}
		raw := json.RawMessage(data)
		jsonParams = &raw
	}

	msg := &Message{
		ID:        &msgID,
		Method:    &method,
		Session:   s,
		Params:    jsonParams,
		Timestamp: time.Now(),
	}

	s.RequestManager.RegisterRequest(&msgID, callback)

	s.UpdateLastActivity()
	s.output <- msg

	return &msgID, nil
}

func (s *BaseSession) SendRequestSync(method string, params interface{}) <-chan *Message {
	resultChan := make(chan *Message, 1)
	pendingRequests := &atomic.Int32{}

	var reader func(msg *Message)
	reader = func(msg *Message) {
		if msg.Result != nil {
			var paginated schema.PaginatedResult
			if err := json.Unmarshal(*msg.Result, &paginated); err == nil {
				if paginated.NextCursor != nil {
					pendingRequests.Add(1)
					s.SendRequest(method, &schema.PaginatedRequestParams{Cursor: paginated.NextCursor}, reader)
				}
			}
		}
		resultChan <- msg
		if pendingRequests.Add(-1) == 0 {
			close(resultChan)
		}
		msg.Processed = true
	}

	pendingRequests.Add(1) // Count the initial request
	_, err := s.SendRequest(method, params, reader)
	if err != nil {
		resultChan <- &Message{
			Error: &JSONRPCError{
				Code:    JSONRPCErrorInternal,
				Message: err.Error(),
			},
		}
		close(resultChan)
	}
	return resultChan
}

// SendResponse sends a response message to the output channel (thread-safe).
// Handles conversion of Go errors to JSONRPCError type for the Message struct.
func (s *BaseSession) SendResponse(msgId *schema.RequestID, result interface{}, err error) {
	if result == nil && err == nil {
		s.Logger.Error("SendResponse called with nil result and nil error", zap.Any("msgId", msgId))
		return
	}

	var jsonResult *json.RawMessage
	var jsonRpcError *JSONRPCError // Use the concrete struct pointer type

	if err != nil {
		// Convert Go error to JSONRPCError structure
		if jsonErr, ok := err.(*JSONRPCError); ok {
			jsonRpcError = jsonErr // Use existing JSONRPCError if passed
		} else {
			jsonRpcError = &JSONRPCError{
				Code:    JSONRPCErrorInternal, // Default internal error
				Message: err.Error(),
			}
		}
		jsonResult = nil // Ensure result is nil when sending an error
		result = nil     // Ensure original result interface is nil too
	} else if result != nil {
		// Marshal the successful result
		data, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			s.Logger.Error("Failed to marshal response result", zap.Error(marshalErr), zap.Any("msgId", msgId))
			// Marshal error becomes the response error
			jsonRpcError = &JSONRPCError{
				Code:    JSONRPCErrorInternal,
				Message: fmt.Sprintf("Failed to marshal result: %v", marshalErr),
			}
			jsonResult = nil
		} else {
			raw := json.RawMessage(data)
			jsonResult = &raw
		}
	}

	msg := &Message{
		Session:   s,
		Timestamp: time.Now(),
		ID:        msgId,
		Result:    jsonResult,
		// Assign the *JSONRPCError (which implements error) to the error interface field
		Error: jsonRpcError,
	}

	s.Mu.RLock()
	outputChan := s.output
	currentStatus := s.status
	s.Mu.RUnlock()

	isInitializeResponse := false
	if result != nil {
		_, isInitializeResponse = result.(schema.InitializeResult)
	}

	if outputChan == nil {
		s.Logger.Warn("Cannot send response, session closed", zap.Any("msgId", msgId))
		return
	}

	if currentStatus != StatusConnected &&
		currentStatus != StatusConnecting && // clients often do not send "notifications/initialized" before sending requests
		!isInitializeResponse {
		s.Logger.Warn("Attempting to send response on non-connected session",
			zap.Any("msgId", msgId),
			zap.Int("status", int(currentStatus)),
			zap.Error(err),
		)
		return
	}

	select {
	case outputChan <- msg:
		s.UpdateLastActivity()
	default:
		s.Logger.Error("Failed to send response, output channel full", zap.Any("msgId", msgId))
	}
}

func (s *BaseSession) Input() *Input {
	return s.inputProcessor
}

func (s *BaseSession) GetLogger() *zap.Logger {
	return s.Logger
}

// SendA2AStreamEvent sends an A2A SSE event to the output channel
func (s *BaseSession) SendA2AStreamEvent(event *A2AStreamEvent) error {
	if event == nil {
		return fmt.Errorf("cannot send nil A2A event")
	}

	msg := &Message{
		Session:   s,
		Timestamp: time.Now(),
		A2AEvent:  event, // Store the event data here
		// ID, Method, Params, Result, Error are nil for SSE events
	}

	s.Mu.RLock()
	outputChan := s.output
	currentStatus := s.status
	s.Mu.RUnlock()

	if outputChan == nil {
		s.Logger.Warn("Cannot send A2A event, session closed")
		return fmt.Errorf("session closed")
	}
	if currentStatus != StatusConnected {
		s.Logger.Warn("Attempting to send A2A event on non-connected session", zap.Int("status", int(currentStatus)))
		return fmt.Errorf("session not connected")
	}
	select {
	case outputChan <- msg:
		s.UpdateLastActivity()
		return nil
	default:
		s.Logger.Error("Failed to send A2A event, output channel full")
		return fmt.Errorf("output channel full")
	}
}
