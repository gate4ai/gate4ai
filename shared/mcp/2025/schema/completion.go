package schema

import "encoding/json"

// CompleteRequest is a request from the client to the server for completion options.
type CompleteRequest struct {
	Method string                  `json:"method"` // const: "completion/complete"
	Params CompletionRequestParams `json:"params"`
}

// CompleteRequestParams contains parameters for completion requests.
type CompletionRequestParams struct {
	Argument CompleteArgument `json:"argument"` // The argument's information
	Ref      json.RawMessage  `json:"ref"`      // Can be PromptReference or ResourceReference
}

// CompleteArgument contains argument information for completions.
type CompleteArgument struct {
	Name  string `json:"name"`  // The name of the argument
	Value string `json:"value"` // The value of the argument to use for completion matching
}

// CompleteResult is the server's response to a completion request.
type CompleteResult struct {
	Meta       map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Completion CompletionInfo         `json:"completion"`
}

// CompletionInfo contains completion results.
type CompletionInfo struct {
	HasMore *bool    `json:"hasMore,omitempty"` // Indicates if there are additional completion options
	Total   *int     `json:"total,omitempty"`   // The total number of completion options available
	Values  []string `json:"values"`            // An array of completion values, max 100 items
}
