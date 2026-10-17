#!/usr/bin/env bash
# Общее для scripts/test-set.sh и scripts/stand.sh: каталог проекта gate4ai (корень,
# где лежат клоны gate4ai/, sdk-go/, relay/, connectors/* ..., или набор worktree
# задачи pitwall с той же раскладкой) и go.work на все его Go-модули.
#
# go.work пишется во временный каталог и подключается через GOWORK, в дереве ничего
# не остаётся: так правка sdk-go сразу видна ядру и коннекторам, как в e2e.

# project_root — каталог проекта: текущий, если в нём есть gate4ai/, иначе родитель
# этого репозитория.
project_root() {
  if [ -d "$PWD/gate4ai" ] && [ -d "$PWD/sdk-go" ]; then
    echo "$PWD"
  else
    (cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
  fi
}

# repos — репозитории проекта (каталоги с .git на глубине 1–2), относительно корня.
repos() {
  (cd "$1" && find . -mindepth 2 -maxdepth 3 -name .git -not -path './.*/*' | sed 's|^\./||; s|/\.git$||' | sort)
}

# go_modules — каталоги go.mod в репозитории (без testdata и vendor).
go_modules() {
  find "$1" -name go.mod -not -path '*/testdata/*' -not -path '*/vendor/*' -not -path '*/node_modules/*' -exec dirname {} \; | sort
}

# use_gowork <корень> — GOWORK на временный go.work со всеми модулями проекта.
use_gowork() {
  local root=$1 dir work version
  work=$(mktemp -d)/go.work
  version=$(sed -n 's/^go //p' "$root/gate4ai/go.mod" | head -n1)
  {
    echo "go $version"
    echo "use ("
    for dir in $(repos "$root"); do
      for m in $(go_modules "$root/$dir"); do echo "	$m"; done
    done
    echo ")"
  } >"$work"
  export GOWORK=$work
}
