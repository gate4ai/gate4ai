module github.com/gate4ai/mcp/gateway

go 1.24.1

replace (
	github.com/gate4ai/mcp/server => ../server
	github.com/gate4ai/mcp/shared => ../shared
	github.com/gate4ai/mcp/tests => ../tests
)

require (
	github.com/gate4ai/mcp/server v0.0.0-00010101000000-000000000000
	github.com/gate4ai/mcp/shared v0.0.0-00010101000000-000000000000
	github.com/gate4ai/mcp/tests v0.0.0-00010101000000-000000000000
	github.com/r3labs/sse/v2 v2.10.0
	go.uber.org/zap v1.27.0
	gopkg.in/cenkalti/backoff.v1 v1.1.0
)

require (
	github.com/lib/pq v1.10.9 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/net v0.33.0 // indirect
	golang.org/x/text v0.24.0 // indirect
	golang.org/x/time v0.11.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
