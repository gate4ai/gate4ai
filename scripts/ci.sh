#!/usr/bin/env bash
# CI: SPDX-заголовки, затем для каждого модуля из go.work — сборка, vet и тесты
# (плюс -race и кросс-сборка под macOS и Windows — локально; в CI её делает матрица build).
set -euo pipefail
cd "$(dirname "$0")/.."
./scripts/spdx.sh --check
# build — сборка без бинарников в дереве: go build ./... сохраняет бинарник,
# только если пакет один и это main (коннекторы) — тогда -o во временный каталог.
build() {
  if [ "$(go list ./... | wc -l)" -eq 1 ]; then
    go build -o "$(mktemp -d)" ./...
  else
    go build ./...
  fi
}
for dir in $(go list -m -f '{{.Dir}}'); do
  echo "== $dir"
  (
    cd "$dir"
    build
    go vet ./...
    go test ./...
    go test -race -short ./...
    # В GitHub Actions (CI=true) под macOS и Windows собирает матрица build в ci.yml.
    if [ -z "${CI:-}" ]; then
      for os in darwin windows; do
        GOOS=$os GOARCH=amd64 build
      done
    fi
    # SDK и коннекторы собираются и в WASM (сторона модуля).
    case "$dir" in
      */sdk-go|*/connectors/*) GOOS=wasip1 GOARCH=wasm go vet ./... ;;
    esac
  )
done
echo "ci: ok"
