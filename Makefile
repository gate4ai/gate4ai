GO ?= go

.PHONY: all build vet test e2e ci bin connectors
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

# Пакеты официальных коннекторов для локальной установки:
#   gate4ai connectors install bin/connectors/<коннектор>.g4c --dev
# Подпись ключом издателя: make connectors KEY=publisher.key
KEY ?=
connectors: bin
	mkdir -p bin/connectors
	for c in mail browser; do \
		./bin/gate4ai connectors pack connectors/$$c $(if $(KEY),--key $(KEY)) -o bin/connectors/$$c.g4c || exit 1; \
	done
