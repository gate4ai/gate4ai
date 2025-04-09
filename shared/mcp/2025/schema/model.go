package schema

import schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"

// ModelHint provides hints for model selection.
// Keys not declared here are currently left unspecified by the spec and are up
// to the client to interpret.
type ModelHint = schema2024.ModelHint

// ModelPreferences expresses the server's preferences for model selection, requested of the client during sampling.
// These preferences are always advisory. The client MAY ignore them.
type ModelPreferences = schema2024.ModelPreferences
