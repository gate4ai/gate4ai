GO ?= go
# Модули workspace (go.work): ядро и будущие отдельные репозитории.
MODULES := $(shell $(GO) list -m -f '{{.Dir}}')

.PHONY: all build vet test ci bin connectors
all: ci

build:
	@for m in $(MODULES); do (cd $$m && $(GO) build ./...) || exit 1; done

vet:
	@for m in $(MODULES); do (cd $$m && $(GO) vet ./...) || exit 1; done

test:
	@for m in $(MODULES); do (cd $$m && $(GO) test ./...) || exit 1; done

ci:
	./scripts/ci.sh

bin:
	mkdir -p bin
	$(GO) build -o bin/ ./cmd/...
	cd relay && $(GO) build -o ../bin/ ./cmd/...

# Пакеты официальных коннекторов для локальной установки:
#   gate4ai connectors install bin/connectors/<коннектор>.g4c --dev
# Подпись ключом издателя: make connectors KEY=publisher.key
KEY ?=
connectors: bin
	mkdir -p bin/connectors
	for c in files mail browser tbank; do \
		./bin/gate4ai connectors pack connectors/$$c $(if $(KEY),--key $(KEY)) -o bin/connectors/$$c.g4c || exit 1; \
	done
