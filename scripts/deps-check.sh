#!/usr/bin/env bash
# Направление зависимостей между частями репозитория (docs/architecture.md):
#   sdk-go/      — только sdk-go (Apache-2.0: AGPL-код не тянет)
#   relay/       — relay и sdk-go
#   ядро         — (cmd/, internal/) ядро, sdk-go; relay — только в тестах
#   connectors/X — свой каталог, sdk-go, ядро; другой коннектор — никогда
#   e2e/         — что угодно; e2e не импортирует никто
# Проверяются прямые импорты пакетов и их тестов под linux, darwin, windows и wasip1:
# если каждое ребро разрешено, запрещённого пути нет и транзитивно.
set -euo pipefail
cd "$(dirname "$0")/.."
# Только этот модуль: с go.work (dev/scripts/test-set.sh) go list -m печатает все модули
# workspace, и ни один импорт не совпал бы с «$mod» — проверка молча прошла бы пустой.
export GOWORK=off
mod=$(go list -m)
case $mod in *[[:space:]]* | "") echo "deps-check: go list -m gave '$mod', not one module" >&2; exit 1 ;; esac
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

# part <путь пакета> — к какой части репозитория он относится.
part() {
  local rel=${1#"$mod"}
  rel=${rel#/}
  case $rel in
    sdk-go | sdk-go/*) echo sdk-go ;;
    relay | relay/*) echo relay ;;
    e2e | e2e/*) echo e2e ;;
    connectors/*) rel=${rel#connectors/}; echo "connector:${rel%%/*}" ;;
    *) echo core ;;
  esac
}

# allowed <часть импортирующего> <часть импортируемого> <test|prod>
allowed() {
  local from=$1 to=$2 kind=$3
  [ "$from" = "$to" ] && return 0
  [ "$to" = sdk-go ] && [ "$from" != sdk-go ] && return 0
  case $from:$to in
    e2e:*) [ "$to" != e2e ] ;;
    core:relay) [ "$kind" = test ] ;;
    connector:*:core) return 0 ;;
    *) return 1 ;;
  esac
}

bad=0
for goos in linux darwin windows wasip1; do
  goarch=amd64
  [ "$goos" = wasip1 ] && goarch=wasm
  # Ошибка go list (пакеты не грузятся) — провал, а не пустой список и «ok».
  if ! GOOS=$goos GOARCH=$goarch go list -e -f '{{if .Error}}{{.ImportPath}}: {{.Error}}
{{end}}' ./... >"$tmp" || [ -s "$tmp" ]; then
    echo "deps-check ($goos): go list failed:" >&2
    cat "$tmp" >&2
    exit 1
  fi
  GOOS=$goos GOARCH=$goarch go list -e -f '{{.ImportPath}} prod {{join .Imports " "}}
{{.ImportPath}} test {{join .TestImports " "}} {{join .XTestImports " "}}' ./... >"$tmp" || {
    echo "deps-check ($goos): go list failed" >&2
    exit 1
  }
  [ -s "$tmp" ] || { echo "deps-check ($goos): no packages" >&2; exit 1; }
  while read -r pkg kind imports; do
    from=$(part "$pkg")
    for imp in $imports; do
      case $imp in "$mod" | "$mod"/*) ;; *) continue ;; esac
      to=$(part "$imp")
      if ! allowed "$from" "$to" "$kind"; then
        echo "deps-check ($goos): $pkg ($from) imports $imp ($to)${kind:+ in $kind}"
        bad=1
      fi
    done
  done <"$tmp"
done
[ "$bad" = 0 ] && echo "deps-check: ok"
exit $bad
