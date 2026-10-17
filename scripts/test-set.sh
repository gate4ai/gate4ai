#!/usr/bin/env bash
# Тесты задачи pitwall в проекте из нескольких репозиториев («gate4ai dev»).
# Запускается из корня проекта или набора worktree; PITWALL_REPOS — изменённые
# репозитории («gate4ai connectors/mail»), без него — все.
#   Go-модуль:  go vet + go test, вместе с соседями через общий go.work;
#   www:        cargo test;
#   e2e:        если изменились sdk-go или хотя бы два Go-репозитория (или сам e2e).
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/workset.sh"
root=$(project_root)
cd "$root"
changed=${PITWALL_REPOS-$(repos "$root" | tr '\n' ' ')}
if [ -z "${changed// /}" ]; then
  echo "test-set: no repository changed, nothing to test"
  exit 0
fi
use_gowork "$root"
go_changed=0
e2e=0
for r in $changed; do
  [ -d "$r" ] || { echo "test-set: no $r in $root" >&2; exit 1; }
  if [ -n "$(go_modules "$r")" ]; then
    go_changed=$((go_changed + 1))
    [ "$r" = sdk-go ] && e2e=1
    [ "$r" = e2e ] && { e2e=1; continue; }
    for m in $(go_modules "$r"); do
      echo "== $m"
      (cd "$m" && go vet ./... && go test ./...)
    done
  elif [ -f "$r/Cargo.toml" ]; then
    echo "== $r"
    (cd "$r" && cargo test --locked)
  else
    echo "== $r: nothing to test"
  fi
done
[ "$go_changed" -ge 2 ] && e2e=1
if [ "$e2e" = 1 ] && [ -d e2e ]; then
  echo "== e2e"
  (cd e2e && go test ./...)
fi
