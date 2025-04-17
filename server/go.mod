module github.com/gate4ai/gate4ai/server

go 1.24.1

replace github.com/gate4ai/gate4ai/shared => ../shared

require (
	github.com/gate4ai/gate4ai/shared v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.8.1
	go.uber.org/zap v1.27.0
	golang.org/x/crypto v0.37.0
	golang.org/x/time v0.11.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/net v0.21.0 // indirect
	golang.org/x/text v0.24.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
