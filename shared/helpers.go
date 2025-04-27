package shared

import (
	"fmt"
	"net/http"

	"go.uber.org/zap"
)

func PointerTo[T any](v T) *T {
	return &v
}

func StringPtrToString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

// NilIfNil returns "nil" if the string pointer is nil, otherwise returns the pointed-to string.
func NilIfNil(s *string) string {
	if s == nil {
		return "nil"
	}
	return *s
}

func FlushIfNotDone(logger *zap.Logger, r *http.Request, w http.ResponseWriter, format string, a ...any) error {
	select {
	case <-r.Context().Done():
		// Request context is done (e.g., client disconnected)
		if logger != nil {
			// Avoid logging noise in normal disconnect scenarios, use Debug level
			logger.Debug("Context done, skipping write/flush", zap.Error(r.Context().Err()))
		}
		return r.Context().Err() // Return the context's error
	default:
		// Context is still active, proceed

		// 1. Attempt to write data
		_, writeErr := fmt.Fprintf(w, format, a...)
		if writeErr != nil {
			// Error during write (e.g., connection closed between check and write)
			if logger != nil {
				logger.Warn("Error writing to response writer", zap.Error(writeErr))
			}
			return fmt.Errorf("write error: %w", writeErr)
		}

		// 2. Attempt to Flush
		if flusher, ok := w.(http.Flusher); ok {
			// ResponseWriter supports Flush
			// Note: flusher.Flush() itself doesn't return an error in the standard interface.
			// It might panic if the underlying connection is closed unexpectedly *after* the write.
			// This function primarily prevents calling Flush *after* the context is known to be done.
			flusher.Flush()
			// Optional debug log for successful flush:
			// if logger != nil {
			//     logger.Debug("Successfully wrote and flushed data")
			// }
			return nil // Success
		}

		// ResponseWriter does not support Flush (unlikely for SSE, but possible)
		if logger != nil {
			logger.Error("ResponseWriter does not support flushing (http.Flusher)")
		}
		return fmt.Errorf("response writer does not support flushing")
	}
}