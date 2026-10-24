GO ?= go

.PHONY: all build vet test e2e ci bin
all: ci

build:
	$(GO) build ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test $$($(GO) list ./... | grep -v '/e2e$$')

e2e:
	./scripts/ci.sh e2e

ci:
	./scripts/ci.sh

bin:
	mkdir -p bin
	$(GO) build -o bin/ ./cmd/... ./relay/cmd/...
