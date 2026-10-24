#!/usr/bin/env bash
# CI: SPDX-заголовки, направление зависимостей, сборка, vet и тесты всего модуля
# (e2e — отдельно: scripts/ci.sh e2e); локально ещё и кросс-сборка под macOS и
# Windows, в GitHub Actions (CI=true) её делает матрица build в ci.yml.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off
if [ "${1:-}" = e2e ]; then
  go test -count=1 ./e2e/...
  echo "ci e2e: ok"
  exit 0
fi
./scripts/spdx.sh --check
./scripts/deps-check.sh
pkgs=$(go list ./... | grep -v '/e2e$')
go build ./...
go vet ./...
go test $pkgs
go test -race -short $pkgs
# Сборка для стенда (GATE4AI_OWNER_CHECK=off действует только в ней).
go vet -tags gate4ai_devstand ./cmd/gate4ai ./internal/front/cli
go test -tags gate4ai_devstand ./internal/platform/agentid
if [ -z "${CI:-}" ]; then
  for os in darwin windows; do
    GOOS=$os GOARCH=amd64 go build ./...
  done
fi
# SDK и WASM-коннекторы собираются и в WASM (сторона модуля).
GOOS=wasip1 GOARCH=wasm go vet ./sdk-go/...
echo "ci: ok"
