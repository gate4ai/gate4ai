package client

type Cursor = string

type Pagination struct {
	Cursor *Cursor `json:"cursor,omitempty"`
}

// PaginatedRequest is the base type for paginated requests.
type PaginatedRequest struct {
	Method string     `json:"method"`
	Params Pagination `json:"params,omitempty"`
}

// PaginatedResult is the base type for paginated results.
type PaginatedResult struct {
	Meta       map[string]interface{} `json:"_meta,omitempty"`      // Reserved for metadata
	NextCursor *Cursor                `json:"nextCursor,omitempty"` // Next page token
}
