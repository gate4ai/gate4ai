#!/usr/bin/env bash
# Разделение монорепозитория на репозитории github.com/gate4ai/* — локально,
# с историей. Ничего не публикует: push делает человек (см. PLAN.md, «Нужно от
# человека»).
#
#   scripts/split.sh [OUT]          — OUT по умолчанию ../gate4ai-split
#   scripts/split.sh --verify [OUT] — только проверка уже разделённых репозиториев
#
# Компоненты — git subtree split по каталогу. Ядро — копия истории без
# выделенных каталогов и go.work, и с LICENSE = FSL-1.1-ALv2 во всех коммитах:
# ранние коммиты были под AGPL, но не публиковались, и в публичной истории ядра
# их лицензия — та же FSL.
#
# Проверка: каждый Go-репозиторий собирается и тестируется отдельно, видя
# только свои зависимости из gate4ai/* (до публикации модулей — через
# временный go.work): ядро — sdk-go и relay (только тесты), коннекторы — sdk-go,
# sdk-go и relay — ничего.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
VERIFY_ONLY=0
if [ "${1:-}" = "--verify" ]; then VERIFY_ONLY=1; shift; fi
OUT=$(realpath -m "${1:-../gate4ai-split}")

# каталог → репозиторий
COMPONENTS=(
  "relay:relay"
  "sdk-go:sdk-go"
  "connectors/mail:gate4ai-mail"
  "connectors/files:gate4ai-files"
  "connectors/browser:gate4ai-browser"
  "tray:tray"
  "registry:registry"
  "github-org:.github"
)
EXTRACTED="relay sdk-go connectors tray registry github-org go.work go.work.sum"

split() {
  if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
    echo "split: commit your changes first" >&2
    exit 1
  fi
  mkdir -p "$OUT"
  for c in "${COMPONENTS[@]}"; do
    dir=${c%%:*}; repo=${c##*:}
    echo "== $repo ($dir)"
    rm -rf "${OUT:?}/$repo"
    branch="split/${repo#.}" # имя ветки не может начинаться с точки
    git subtree split --quiet --prefix="$dir" -b "$branch" >/dev/null
    git init --quiet -b main "$OUT/$repo"
    git -C "$OUT/$repo" pull --quiet "$ROOT" "$branch"
    git branch -D --quiet "$branch"
  done
  echo "== gate4ai (core)"
  rm -rf "$OUT/gate4ai"
  git clone --quiet --no-local --single-branch "$ROOT" "$OUT/gate4ai"
  lic=$(git -C "$OUT/gate4ai" hash-object -w "$ROOT/LICENSE")
  (
    cd "$OUT/gate4ai"
    git branch -m main 2>/dev/null || true
    FILTER_BRANCH_SQUELCH_WARNING=1 git filter-branch --force --prune-empty --index-filter "
      git rm -r --cached --ignore-unmatch --quiet $EXTRACTED
      git update-index --add --cacheinfo 100644,$lic,LICENSE
    " HEAD >/dev/null
    git remote remove origin
    rm -rf .git/refs/original
    git reflog expire --expire=now --all
    git gc --quiet --prune=now
  )
}

# check <repo> <deps...> — сборка и тесты репозитория со своими зависимостями.
check() {
  local repo=$1; shift
  local ws
  ws=$(mktemp -d)
  {
    echo "go 1.27.1"
    echo "use ("
    echo "  $OUT/$repo"
    for d in "$@"; do echo "  $OUT/$d"; done
    echo ")"
  } > "$ws/go.work"
  echo "== verify $repo (with: ${*:-nothing})"
  (
    cd "$OUT/$repo"
    export GOWORK="$ws/go.work"
    if [ "$(go list ./... | wc -l)" -eq 1 ]; then go build -o "$ws" ./...; else go build ./...; fi
    go vet ./...
    go test ./...
  )
  rm -rf "$ws"
}

verify() {
  # В ядре нет кода коннекторов и выделенных каталогов.
  for d in relay sdk-go connectors tray registry github-org; do
    if [ -e "$OUT/gate4ai/$d" ]; then echo "core still has $d" >&2; exit 1; fi
  done
  if grep -rl "SPDX-License-Identifier: AGPL" "$OUT/gate4ai" --include='*.go' >/dev/null; then
    echo "core contains AGPL sources" >&2; exit 1
  fi
  if git -C "$OUT/gate4ai" log --all --format=%H | while read -r c; do git -C "$OUT/gate4ai" show "$c:LICENSE" 2>/dev/null | head -1; done | grep -q "GNU AFFERO"; then
    echo "core history still has the AGPL LICENSE" >&2; exit 1
  fi
  check sdk-go
  check relay
  check gate4ai-mail sdk-go
  check gate4ai-files sdk-go
  check gate4ai-browser sdk-go
  check gate4ai sdk-go relay
  echo "split: ok — $OUT"
}

[ "$VERIFY_ONLY" = 1 ] || split
verify
