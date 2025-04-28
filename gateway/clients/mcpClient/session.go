package mcpClient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients/mcpClient/capability"
	"github.com/gate4ai/gate4ai/shared"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"github.com/r3labs/sse/v2"
	"go.uber.org/zap"
	"gopkg.in/cenkalti/backoff.v1"
)

var _ shared.ISession = (*Session)(nil)

// Session represents a connection to a single backend MCP server.
type Session struct {
	*shared.BaseSession
	Locker                       sync.RWMutex
	Backend                      *Backend
	postEndpoint                 string
	ctx                          context.Context
	sseClient                    *sse.Client
	httpClient                   *http.Client
	sseCh                        chan *sse.Event
	closeCh                      chan struct{}
	initialization               chan error
	initializationClosed         bool
	serverInfo                   *schema.Implementation
	tools                        []schema.Tool
	toolsInitialized             bool
	prompts                      []schema.Prompt
	promptsInitialized           bool
	resources                    []schema.Resource
	resourcesInitialized         bool
	resourceTemplates            []schema.ResourceTemplate
	resourceTemplatesInitialized bool
	inputProcessor               *shared.Input
	SamplingCapability           *capability.SamplingCapability
	ResourcesCapability          *capability.ResourcesCapability
	ResourceTemplatesCapability  *capability.ResourceTemplatesCapability
	currentHeaders               map[string]string
}

func (s *Session) GetCurrentHeaders() map[string]string { /* ... as before ... */
	s.Locker.RLock()
	defer s.Locker.RUnlock()
	headersCopy := make(map[string]string, len(s.currentHeaders))
	for k, v := range s.currentHeaders {
		headersCopy[k] = v
	}
	return headersCopy
}
func (s *Session) writeInitializationErrorAndClose(newErr error) { /* ... as before ... */
	s.Locker.Lock()
	defer s.Locker.Unlock()
	if s.initializationClosed {
		if newErr != nil {
			s.BaseSession.Logger.Warn("Init channel closed, discarding secondary error", zap.Error(newErr))
		}
		return
	}
	if s.initialization == nil {
		s.BaseSession.Logger.Error("Internal state error: initClosed false but init chan nil")
		s.initializationClosed = true
		return
	}
	if newErr != nil {
		select {
		case s.initialization <- newErr:
			s.BaseSession.Logger.Error("Signaling initialization failure", zap.Error(newErr))
		default:
			s.BaseSession.Logger.Warn("Init channel contained error or closing; discarding secondary error", zap.Error(newErr))
		}
	}
	close(s.initialization)
	s.initializationClosed = true
	s.BaseSession.Logger.Debug("Initialization channel closed.")
}

func (s *Session) Open() chan error {
	logger := s.BaseSession.Logger
	logger.Debug("Open() called")
	s.Locker.Lock()
	if s.GetStatus() == shared.StatusConnected {
		logger.Debug("Session already connected")
		if s.initialization != nil {
			s.Locker.Unlock()
			return s.initialization
		}
		closedChan := make(chan error)
		close(closedChan)
		s.initialization = closedChan
		s.Locker.Unlock()
		return closedChan
	}
	if s.initialization != nil {
		logger.Debug("Initialization already in progress")
		s.Locker.Unlock()
		return s.initialization
	}

	logger.Info("Starting new session initialization")
	s.initialization = make(chan error, 1)
	s.initializationClosed = false
	s.SetStatus(shared.StatusConnecting)
	if s.closeCh == nil {
		s.closeCh = make(chan struct{})
	}
	s.postEndpoint = ""
	s.serverInfo = nil
	s.Locker.Unlock()

	logger.Debug("Subscribing to SSE channel")
	sseContext, sseCancel := context.WithCancel(s.ctx) // sseCancel defined here
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.MaxElapsedTime = 0
	s.sseClient.ReconnectStrategy = backoff.WithContext(expBackoff, sseContext)
	s.sseClient.ReconnectNotify = func(err error, t time.Duration) {
		logger.Warn("SSE connection error, retrying...", zap.Error(err), zap.Duration("delay", t))
		if stopWords(err.Error()) {
			logger.Debug("Stopping SSE connection attempts")
			sseCancel()
		}
	}

	err := s.sseClient.SubscribeChanWithContext(sseContext, "", s.sseCh)
	if err != nil {
		logger.Warn("Failed to subscribe to SSE events", zap.Error(err))
		sseCancel() 
		s.SetStatus(shared.StatusNew)
		s.writeInitializationErrorAndClose(fmt.Errorf("SSE subscription failed: %w", err))
		return s.initialization
	}
	logger.Debug("SSE subscription initiated")

	if s.Input() == nil {
		logger.Error("Input is nil")
		sseCancel()
		s.writeInitializationErrorAndClose(errors.New("input is nil"))
		return s.initialization
	}

	go s.processLoop(sseCancel) // Pass cancel func

	return s.initialization
}

func stopWords(errMsg string) bool {
    return strings.Contains(errMsg, "Unauthorized") ||
	strings.Contains(errMsg, "no such host") ||
	strings.Contains(errMsg, "connection refused") ||
	strings.Contains(errMsg, "cannot resolve") ||
	strings.Contains(errMsg, "unknown host") ||
	strings.Contains(errMsg, "lookup") 
}

func (s *Session) processLoop(sseCancel context.CancelFunc) { /* ... as before ... */
	loopLogger := s.BaseSession.Logger.With(zap.String("goroutine", "processLoop"))
	loopLogger.Debug("Starting session processing loop")
	defer func() {
		loopLogger.Info("Session processing loop ended")
		sseCancel()
		s.Locker.Lock()
		if s.sseClient != nil {
			s.sseClient.Unsubscribe(s.sseCh)
		}
		s.Locker.Unlock()
		s.SetStatus(shared.StatusNew)
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
				return
			}
			if event == nil {
				loopLogger.Warn("Received nil event from SSE channel, skipping")
				continue
			}
			loopLogger.Debug("Received SSE event", zap.String("eventID", string(event.ID)), zap.String("eventName", string(event.Event)))
			switch string(event.Event) {
			case "endpoint":
				s.Locker.RLock()
				endpointSet := s.postEndpoint != ""
				s.Locker.RUnlock()
				if !endpointSet {
					if len(event.Data) == 0 {
						loopLogger.Error("Received endpoint event with empty data")
						s.writeInitializationErrorAndClose(errors.New("empty endpoint data"))
						return
					}
					postURLStr := string(event.Data)
					postURL, err := url.Parse(postURLStr)
					if err != nil {
						loopLogger.Error("Failed to parse postUrl", zap.Error(err))
						s.writeInitializationErrorAndClose(fmt.Errorf("invalid endpoint URL: %w", err))
						return
					}
					s.Locker.Lock()
					s.postEndpoint = s.Backend.URL.ResolveReference(postURL).String()
					s.Locker.Unlock()
					loopLogger.Info("Received POST endpoint", zap.String("endpoint", s.postEndpoint))
					go s.sendInitialize()
				} else {
					loopLogger.Debug("Ignoring subsequent endpoint event")
				}
			case "message":
				if len(event.Data) == 0 {
					loopLogger.Warn("Received message event with empty data, skipping")
					continue
				}
				msgs, err := shared.ParseMessages(s, event.Data)
				if err != nil {
					loopLogger.Error("Failed to parse JSON-RPC message from SSE", zap.Error(err))
					continue
				}
				for _, msg := range msgs {
					s.Input().Put(msg)
				}
			case "ping":
				loopLogger.Debug("Received ping event")
			default:
				loopLogger.Warn("Received unknown SSE event type", zap.String("eventName", string(event.Event)))
			}
		case <-s.closeCh:
			loopLogger.Info("Session explicitly closed via closeCh")
			return
		case <-s.ctx.Done():
			loopLogger.Info("Session context cancelled", zap.Error(s.ctx.Err()))
			return
		}
	}
}
func (s *Session) Close() error { /* ... as before ... */
	logger := s.BaseSession.Logger
	logger.Info("Close() called")
	s.Locker.Lock()
	if s.closeCh == nil {
		s.Locker.Unlock()
		logger.Debug("Session already closed")
		return nil
	}
	select {
	case <-s.closeCh:
		logger.Debug("closeCh already closed")
	default:
		close(s.closeCh)
		s.closeCh = nil
		logger.Debug("Signaled processing loop")
	}
	s.Locker.Unlock()
	s.SetStatus(shared.StatusNew)
	baseErr := s.BaseSession.Close()
	if baseErr != nil {
		logger.Error("Error closing BaseSession Output", zap.Error(baseErr))
	} else {
		logger.Debug("BaseSession Output closed")
	}
	logger.Info("Session close process completed")
	return baseErr
}
