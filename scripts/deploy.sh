#!/usr/bin/env bash
# Шаги деплоя gate4ai для процесса pitwall «gate4ai deploy» (цели — в настройке папки
# «Deploy targets», порядок целей — порядок выкатки). Запускается в каталоге репозитория
# цели; PITWALL_ROOT — общий каталог с клонами (по умолчанию — тот, где лежит gate4ai/ с этим скриптом).
# Каждый шаг ждёт свой CI и падает (код ≠ 0), если что-то не вышло; повтор безопасен:
# уже сделанное он пропускает.
#
#   deploy.sh push [ветка]        отправить ветку (gate4ai-alex), дождаться ci.yml этого коммита
#   deploy.sh tag                 следующий патч-тег vX.Y.Z+1 на отправленном main, дождаться релиза
#   deploy.sh connector           версия в manifest.yaml +1, коммит, тег v<версия>, push, дождаться релиза
#   deploy.sh registry            записать выпущенную версию коннектора в registry: add + sign, PR, CI, слияние
#   deploy.sh in-registry         проверка: версия коннектора из manifest.yaml есть в registry (origin/main)
#   deploy.sh sdk-current         проверка: go.mod требует последний тег sdk-go, и в sdk-go нет коммитов после него
#   deploy.sh sdk-bump            go.mod на последний тег sdk-go (go get + tidy), vet и тесты без go.work, коммит
#   deploy.sh local               дождаться, что gate4.ai/latest отдаёт последний тег ядра, и обновить
#                                 локальную установку тем же install.sh, что у пользователей
set -euo pipefail
root=${PITWALL_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}
repo=$(git rev-parse --show-toplevel)
cd "$repo"
# Токен аккаунта с правом push (gh-push.sh): для git push и записи через gh.
# shellcheck source=gh-push.sh
. "$(dirname "$0")/gh-push.sh"
writer() { GH_TOKEN=$T "$@"; }
slug=$(git remote get-url origin | sed -E 's#.*github\.com[:/]##; s#\.git$##')
say() { echo "deploy: $*"; }
die() { echo "deploy: $*" >&2; exit 1; }

# wait_run <workflow> <sha> [ref] — ждать запуск workflow на коммите и его успех; ref — ветка
# или тег запуска, когда у коммита их несколько (main и тег релиза — разные запуски ci.yml).
wait_run() {
  local workflow=$1 sha=$2 ref=${3:-} id="" i
  for i in $(seq 1 60); do
    id=$(writer gh run list -R "$slug" --workflow "$workflow" --commit "$sha" ${ref:+--branch "$ref"} --limit 1 --json databaseId -q '.[0].databaseId' 2>/dev/null || true)
    [ -n "$id" ] && break
    sleep 5
  done
  [ -n "$id" ] || die "no run of $workflow for ${sha:0:7}${ref:+ ($ref)} in $slug after 5 minutes"
  say "waiting for $workflow run $id (https://github.com/$slug/actions/runs/$id)"
  writer gh run watch "$id" -R "$slug" --exit-status --interval 15 >/dev/null || {
    writer gh run view "$id" -R "$slug" --log-failed 2>/dev/null | tail -40 >&2 || true
    die "$workflow run $id failed: https://github.com/$slug/actions/runs/$id"
  }
  say "$workflow run $id passed"
}

has_workflow() { [ -f ".github/workflows/$1" ]; }

push() {
  local branch=${1:-$(git branch --show-current)}
  git fetch -q origin "$branch"
  if [ "$(git rev-list --count "origin/$branch..$branch")" = 0 ]; then
    say "$branch is pushed already"
  else
    say "pushing $branch of $slug"
    gpush "$repo" origin "$branch"
  fi
  has_workflow ci.yml && wait_run ci.yml "$(git rev-parse "$branch")"
  return 0
}

tag() {
  push main
  local last next
  last=$(git describe --tags --abbrev=0 main 2>/dev/null || echo v0.0.0)
  if [ "$(git rev-list --count "$last..main")" = 0 ]; then
    say "main is tagged already ($last)"
    next=$last
  else
    next=$(echo "$last" | awk -F. -v OFS=. '{ $NF = $NF + 1; print }')
    say "tagging $next (after $last)"
    git tag -a "$next" -m "$next" main
    gpush "$repo" origin "$next"
  fi
  has_workflow release.yml && wait_run release.yml "$(git rev-list -n 1 "$next")"
  return 0
}

connector() {
  local version
  version=$(sed -nE 's/^version: *"?([0-9]+)"?.*/\1/p' manifest.yaml)
  [ -n "$version" ] || die "no version in manifest.yaml"
  if git rev-parse -q --verify "refs/tags/v$version" >/dev/null && [ "$(git rev-list --count "v$version..main")" != 0 ]; then
    version=$((version + 1))
    say "manifest version $version"
    sed -i -E "s/^version: *\"?[0-9]+\"?/version: \"$version\"/" manifest.yaml
    git commit -q -m "v$version" manifest.yaml
  fi
  push main
  if ! git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
    git tag -a "v$version" -m "v$version" main
  fi
  gpush "$repo" origin "v$version"
  wait_run ci.yml "$(git rev-list -n 1 "v$version")" "v$version"
  writer gh release view "v$version" -R "$slug" >/dev/null || die "no release v$version in $slug"
}

connector_name() { sed -nE 's/^connector: *"?([a-z0-9-]+)"?.*/\1/p' manifest.yaml; }

# listed <имя> <версия> — есть ли версия коннектора в индексе registry на origin/main.
listed() {
  git -C "$root/registry" show origin/main:index.json |
    jq -e --arg n "$1" --arg v "$2" '.connectors[$n].versions[]? | select(.version == $v)' >/dev/null
}

in_registry() {
  local name version
  name=$(connector_name)
  version=$(sed -nE 's/^version: *"?([0-9]+)"?.*/\1/p' manifest.yaml)
  if listed "$name" "$version"; then say "$name $version is in the registry"; else die "$name $version is not in the registry"; fi
}

registry() {
  local name version reg work branch pr
  name=$(connector_name)
  version=$(git describe --tags --abbrev=0 main | sed 's/^v//')
  reg=$root/registry
  git -C "$reg" fetch -q origin main
  if listed "$name" "$version"; then
    say "$name $version is in the registry already"
    return 0
  fi
  branch="$name-v$version"
  work=$(mktemp -d)/registry
  git -C "$reg" worktree add -q -B "$branch" "$work" origin/main
  # Значения подставлены сразу: trap сработает при выходе из скрипта, когда local уже нет (set -u).
  trap "git -C $(printf %q "$reg") worktree remove --force $(printf %q "$work") >/dev/null 2>&1 || true" EXIT
  writer gh release download "v$version" -R "$slug" -p pack.json -D "$work/.." --clobber
  (cd "$work" && gate4ai registry add index.json ../pack.json --publisher gate4ai \
    && gate4ai registry sign index.json --key "$root/.secrets/keys/registry.key")
  git -C "$work" commit -q -s -am "$name v$version" # -s: registry принимает коммиты с DCO
  gpush "$work" -f origin "$branch"
  pr=$(writer gh pr list -R gate4ai/registry --head "$branch" --json number -q '.[0].number')
  [ -n "$pr" ] || pr=$(writer gh pr create -R gate4ai/registry --head "$branch" --title "$name v$version" --body "Release v$version of $slug." | sed -E 's#.*/##')
  say "waiting for the checks of registry PR #$pr"
  # Сразу после push у PR ещё нет проверок, и gh pr checks падает: ждать запуск Actions.
  local i
  for i in $(seq 1 60); do
    [ "$(writer gh pr view "$pr" -R gate4ai/registry --json statusCheckRollup -q '[.statusCheckRollup[] | select(.workflowName != "")] | length' 2>/dev/null || echo 0)" != 0 ] && break
    sleep 5
  done
  writer gh pr checks "$pr" -R gate4ai/registry --watch --interval 15 >/dev/null || die "registry PR #$pr: checks failed"
  writer gh pr merge "$pr" -R gate4ai/registry --merge --delete-branch
  git -C "$reg" fetch -q origin main
  say "$name $version is in the registry"
}

# sdk_required — версия github.com/gate4ai/sdk-go в go.mod репозитория.
sdk_required() {
  awk '$1 == "github.com/gate4ai/sdk-go" { print $2 } $1 == "require" && $2 == "github.com/gate4ai/sdk-go" { print $3 }' go.mod
}

# sdk_latest — последний тег sdk-go; падает, если в main sdk-go есть коммиты после него
# (их ещё выпустят тегом — значит, обновление go.mod впереди).
sdk_latest() {
  local sdk=$root/sdk-go latest
  latest=$(git -C "$sdk" describe --tags --abbrev=0 main)
  [ "$(git -C "$sdk" rev-list --count "$latest..main")" = 0 ] || die "sdk-go has commits after $latest, not tagged yet"
  echo "$latest"
}

sdk_current() {
  local want have
  want=$(sdk_latest)
  have=$(sdk_required)
  [ -n "$have" ] || die "go.mod does not require github.com/gate4ai/sdk-go"
  [ "$have" = "$want" ] || die "requires sdk-go $have, the latest is $want"
  say "requires sdk-go $want"
}

sdk_bump() {
  local want have i
  want=$(sdk_latest)
  have=$(sdk_required)
  if [ "$have" = "$want" ]; then
    say "requires sdk-go $want already"
    return 0
  fi
  [ -z "$(git status --porcelain)" ] || die "$repo has changes not committed"
  say "sdk-go $have -> $want"
  # Свежий тег доходит до proxy.golang.org не сразу.
  for i in $(seq 1 20); do
    GOWORK=off go get "github.com/gate4ai/sdk-go@$want" && break
    [ "$i" = 20 ] && die "go get github.com/gate4ai/sdk-go@$want failed for 5 minutes"
    sleep 15
  done
  GOWORK=off go mod tidy
  # Так, как соберёт CI: с опубликованным sdk-go, без соседних клонов из go.work.
  say "go vet and go test with sdk-go $want"
  GOWORK=off go vet ./...
  GOWORK=off go test ./...
  git commit -q -m "sdk-go $want" go.mod go.sum
  say "committed: sdk-go $want"
}

local_install() {
  local want got i
  want=$(git -C "$root/gate4ai" describe --tags --abbrev=0 main)
  for i in $(seq 1 60); do
    got=$(curl -fsS --max-time 10 https://gate4.ai/latest 2>/dev/null | tr -d '[:space:]' || true)
    [ "$got" = "$want" ] && break
    [ "$i" = 1 ] && say "waiting for https://gate4.ai/latest to give $want (now: ${got:-nothing})"
    sleep 20
  done
  [ "$got" = "$want" ] || die "https://gate4.ai/latest still gives ${got:-nothing}, not $want, after 20 minutes"
  if [ "v$(gate4ai --version 2>/dev/null | awk '{print $2}')" = "$want" ]; then
    say "the local gate4ai is $want already"
    return 0
  fi
  say "installing $want with https://gate4.ai/install.sh"
  curl -fsSL https://gate4.ai/install.sh | sh
  [ "v$(gate4ai --version | awk '{print $2}')" = "$want" ] || die "after install.sh gate4ai --version is $(gate4ai --version)"
  gate4ai daemon status >/dev/null || die "the daemon does not answer after the install"
  say "the local gate4ai is $want and runs"
}

case "${1:-}" in
  push) push "${2:-}" ;;
  tag) tag ;;
  connector) connector ;;
  registry) registry ;;
  in-registry) in_registry ;;
  sdk-current) sdk_current ;;
  sdk-bump) sdk_bump ;;
  local) local_install ;;
  *) die "usage: deploy.sh push [branch] | tag | connector | registry | in-registry | sdk-current | sdk-bump | local" ;;
esac
