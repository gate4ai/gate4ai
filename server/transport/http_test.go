package transport_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Helper to create a minimal http.Handler for testing
func createDummyMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func TestStartHTTPServer_HTTPMode(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.NewInternalConfig()
	cfg.ServerAddress = "localhost:0"
	cfg.SSLEnabledValue = false

	mux := createDummyMux()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, errChan, err := transport.StartHTTPServer(ctx, logger, cfg, mux, "")
	require.NoError(t, err)
	require.NotNil(t, server)
	require.NotNil(t, errChan)
	defer server.Shutdown(context.Background()) // Ensure shutdown

	// Check server address (will have a dynamic port)
	assert.True(t, strings.HasPrefix(server.Addr, "localhost:"))
	assert.Nil(t, server.TLSConfig, "TLSConfig should be nil in HTTP mode")

	// Check listener error channel (should remain open or close without error initially)
	select {
	case err := <-errChan:
		t.Fatalf("Listener unexpectedly failed immediately: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Expected behavior - no immediate error
	}
}

func TestStartHTTPServer_ManualTLSMode(t *testing.T) {
	// Create test directory for certificates if it doesn't exist
	testDataDir := "../tests/testdata"
	certFile := testDataDir + "/cert.pem"
	keyFile := testDataDir + "/key.pem"

	if err := os.MkdirAll(testDataDir, 0755); err != nil {
		t.Skip("Could not create test directory:", err)
	}

	// Since we need valid certificate files to test this properly
	// we'll only check that the parameters are validated correctly

	logger := zap.NewNop()
	cfg := config.NewInternalConfig()
	cfg.ServerAddress = "localhost:0"
	cfg.SSLEnabledValue = true
	cfg.SSLModeValue = "manual"
	cfg.SSLCertFileValue = certFile
	cfg.SSLKeyFileValue = keyFile

	mux := createDummyMux()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// This should fail with file not found if the cert files don't exist
	_, listenerErrChan, err := transport.StartHTTPServer(ctx, logger, cfg, mux, "")
	assert.NoError(t, err, "Should pass all sync checks")
	err = <-listenerErrChan
	assert.Error(t, err, "http.Server should fail if cert/key files don't exist")
	assert.Contains(t, err.Error(), "cert")
}

func TestStartHTTPServer_ACMEMode(t *testing.T) {
	logger := zap.NewNop()
	cfg := config.NewInternalConfig()
	cfg.ServerAddress = "localhost:0"
	cfg.SSLEnabledValue = true
	cfg.SSLModeValue = "acme"
	cfg.SSLAcmeDomainsValue = []string{"example.com", "www.example.com"}
	cfg.SSLAcmeEmailValue = "test@example.com"
	cfg.SSLAcmeCacheDirValue = t.TempDir() // Use temp dir for cache

	mux := createDummyMux()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, errChan, err := transport.StartHTTPServer(ctx, logger, cfg, mux, "")
	require.NoError(t, err)
	require.NotNil(t, server)
	require.NotNil(t, errChan)
	defer server.Shutdown(context.Background())

	// Verify TLSConfig is set and uses autocert
	require.NotNil(t, server.TLSConfig, "TLSConfig should be set for ACME mode")
	assert.NotNil(t, server.TLSConfig.GetCertificate, "GetCertificate should be set by autocert")
}

func TestStartHTTPServer_MissingParameters(t *testing.T) {
	t.Run("NilLogger", func(t *testing.T) {
		cfg := config.NewInternalConfig()
		mux := createDummyMux()
		_, _, err := transport.StartHTTPServer(context.Background(), nil, cfg, mux, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "logger cannot be nil")
	})

	t.Run("NilConfig", func(t *testing.T) {
		logger := zap.NewNop()
		mux := createDummyMux()
		_, _, err := transport.StartHTTPServer(context.Background(), logger, nil, mux, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config cannot be nil")
	})

	t.Run("NilHandler", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := config.NewInternalConfig()
		_, _, err := transport.StartHTTPServer(context.Background(), logger, cfg, nil, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "http handler")
	})
}

func TestShutdownHTTPServer(t *testing.T) {
	logger := zap.NewNop()
	srv := &http.Server{
		Addr: "localhost:0", // Use ephemeral port
	}

	// Start the server in a goroutine
	go func() {
		_ = srv.ListenAndServe()
	}()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Test shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	transport.ShutdownHTTPServer(ctx, logger, srv)

	// Test nil server case (should not panic)
	transport.ShutdownHTTPServer(ctx, logger, nil)
}
