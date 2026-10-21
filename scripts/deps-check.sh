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
mod=$(go list -m)

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
  done < <(GOOS=$goos GOARCH=$goarch go list -e -f '{{.ImportPath}} prod {{join .Imports " "}}
{{.ImportPath}} test {{join .TestImports " "}} {{join .XTestImports " "}}' ./... 2>/dev/null)
done
[ "$bad" = 0 ] && echo "deps-check: ok"
exit $bad
