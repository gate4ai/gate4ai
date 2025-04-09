package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
	"golang.org/x/crypto/acme/autocert"
)

// StartHTTPServer starts the HTTP/HTTPS server based on the provided configuration.
// It returns the server instance and a channel that signals listener errors after startup.
// An immediate error is returned if setup fails before starting the listener.
func StartHTTPServer(ctx context.Context, logger *zap.Logger, cfg config.IConfig, mux http.Handler, overwriteListenAddr string) (*http.Server, <-chan error, error) {
	if logger == nil {
		return nil, nil, errors.New("logger cannot be nil")
	}
	if cfg == nil {
		return nil, nil, errors.New("config cannot be nil")
	}
	if mux == nil {
		return nil, nil, errors.New("http handler (mux) cannot be nil")
	}

	// --- Determine Listen Address ---
	listenAddr := overwriteListenAddr
	if listenAddr == "" {
		var err error
		listenAddr, err = cfg.ListenAddr()
		if err != nil {
			logger.Error("Failed to get listen address from config", zap.Error(err))
			return nil, nil, fmt.Errorf("failed to get listen address: %w", err)
		}
	}

	// --- Create HTTP Server Instance ---
	server := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
		// Add timeouts for production robustness
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second, // Longer for potential SSE streams
		IdleTimeout:  90 * time.Second,
		BaseContext:  func(_ net.Listener) context.Context { return ctx }, // Propagate context
	}

	// --- Configure SSL/TLS ---
	sslEnabled, err := cfg.SSLEnabled()
	if err != nil {
		logger.Warn("Failed to read SSL enabled setting, assuming disabled", zap.Error(err))
		sslEnabled = false
	}

	var tlsConfig *tls.Config
	var certFile, keyFile string // Only for manual mode
	isACME := false

	if sslEnabled {
		sslMode, _ := cfg.SSLMode() // Ignore error, defaults to "manual"

		if sslMode == "acme" {
			// --- ACME / Let's Encrypt ---
			isACME = true
			domains, err := cfg.SSLAcmeDomains()
			if err != nil || len(domains) == 0 {
				return nil, nil, fmt.Errorf("ACME mode requires at least one domain in config (key 'ssl_acme_domains'): %w", err)
			}
			email, _ := cfg.SSLAcmeEmail() // Email is optional but recommended
			cacheDir, err := cfg.SSLAcmeCacheDir()
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get ACME cache directory: %w", err)
			}
			if err := os.MkdirAll(cacheDir, 0700); err != nil {
				return nil, nil, fmt.Errorf("failed to create ACME cache directory '%s': %w", cacheDir, err)
			}

			certManager := autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				HostPolicy: autocert.HostWhitelist(domains...),
				Email:      email,
				Cache:      autocert.DirCache(cacheDir),
			}
			tlsConfig = certManager.TLSConfig()
			// ACME requires HTTP challenge, redirect HTTP to HTTPS
			// Start a helper HTTP server for challenges IF using ACME
			go func() {
				httpChallengeServer := &http.Server{
					Addr:    ":80",                        // Standard HTTP port for challenges
					Handler: certManager.HTTPHandler(nil), // Handles ACME challenges
				}
				logger.Info("Starting ACME HTTP challenge listener", zap.String("addr", ":80"))
				if err := httpChallengeServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("ACME HTTP challenge listener error", zap.Error(err))
				}
			}()

		} else {
			// --- Manual Mode ---
			certFile, err = cfg.SSLCertFile()
			if err != nil || certFile == "" {
				return nil, nil, fmt.Errorf("manual SSL mode requires a certificate file path (config key 'ssl_cert_file'): %w", err)
			}
			keyFile, err = cfg.SSLKeyFile()
			if err != nil || keyFile == "" {
				return nil, nil, fmt.Errorf("manual SSL mode requires a private key file path (config key 'ssl_key_file'): %w", err)
			}
			// Manual mode doesn't require a specific tls.Config here, ListenAndServeTLS handles it
		}
		server.TLSConfig = tlsConfig // Assign TLS config if ACME, nil otherwise for manual
	}

	// Channel to report listener errors occurring *after* startup
	listenerErrChan := make(chan error, 1)

	// --- Start Server Goroutine ---
	go func() {
		defer close(listenerErrChan) // Close channel when listener exits

		if sslEnabled {
			logger.Info("Starting HTTPS Server", zap.String("addr", listenAddr), zap.Bool("isACME", isACME))
			var err error
			if isACME {
				// ListenAndServeTLS handles challenges via TLSConfig if set
				err = server.ListenAndServeTLS("", "")
			} else {
				err = server.ListenAndServeTLS(certFile, keyFile)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("HTTPS Server listener error", zap.Error(err))
				listenerErrChan <- err
			} else {
				logger.Info("HTTPS Server listener stopped gracefully.")
			}
		} else {
			logger.Info("Starting HTTP Server", zap.String("addr", listenAddr))
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("HTTP Server listener error", zap.Error(err))
				listenerErrChan <- err
			} else {
				logger.Info("HTTP Server listener stopped gracefully.")
			}
		}
	}()

	return server, listenerErrChan, nil
}

// ShutdownHTTPServer attempts a graceful shutdown of the HTTP server.
func ShutdownHTTPServer(ctx context.Context, logger *zap.Logger, server *http.Server) {
	if server == nil {
		logger.Warn("Shutdown requested but server instance is nil")
		return
	}
	logger.Info("Attempting graceful shutdown of HTTP/S server...")
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("HTTP/S server graceful shutdown failed", zap.Error(err))
		// Force close if graceful shutdown fails? Optional.
		// server.Close()
	} else {
		logger.Info("HTTP/S server shut down gracefully.")
	}
}
