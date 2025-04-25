package schema

import (
	schema2024 "github.com/gate4ai/gate4ai/shared/mcp/2024/schema"
)

type RequestID = schema2024.RequestID

func RequestID_FromUInt64(value uint64) RequestID {
	return schema2024.RequestID_FromUInt64(value)
}
