package schema

import schema2024 "github.com/gate4ai/gate4ai/shared/mcp/2024/schema"

// Cursor is an opaque token used to represent a cursor for pagination.
type Cursor = schema2024.Cursor

// PaginatedRequest represents parameters for a request supporting pagination.
type PaginatedRequestParams struct {
	// An opaque token representing the current pagination position.
	// If provided, the server should return results starting after this cursor.
	Cursor *Cursor `json:"cursor,omitempty"`
}

// PaginatedResult represents fields in a response supporting pagination.
type PaginatedResult = schema2024.PaginatedResult
