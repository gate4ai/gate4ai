#!/usr/bin/env bash
# Dev-стенд gate4ai для задачи pitwall («gate4ai dev»): daemon, собранный из исходников
# проекта (или набора worktree), на 127.0.0.1:$PORT со своими данными (.stand/ в корне)
# и сокетами, и коннекторы из папок connectors/* с --watch (пересобираются при правках).
# Установленный gate4ai (локальный прод: ~/.local/bin, его данные, сокеты, связка
# ключей, трей) стенд не трогает. Ctrl+C / SIGTERM останавливают всё.
#   PORT=7531           порт Web UI (pitwall даёт свободный)
#   STAND_PUBLIC=1      ещё и зарегистрировать стенд в relay.gate4.ai: адрес для claude.ai
#                       (каждая новая регистрация — новый сертификат на *.relay.gate4.ai,
#                       лимит Let's Encrypt 50 в неделю; данные .stand/ её сохраняют)
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/workset.sh"
root=$(project_root)
cd "$root"
port=${PORT:-7531}
stand=$root/.stand
mkdir -p "$stand/bin"
use_gowork "$root"
echo "stand: building gate4ai from $root/gate4ai"
(cd gate4ai && go build -o "$stand/bin/" ./cmd/gate4ai)
export PATH="$stand/bin:$PATH"
export GATE4AI_HOME=$stand/home
# Сокеты — в коротком пути: у unix-сокета лимит длины, а набор worktree лежит глубоко.
run=$(mktemp -d /tmp/g4stand.XXXXXX)
export GATE4AI_SOCKET=$run/owner.sock GATE4AI_AGENT_SOCKET=$run/agent.sock
pids=()
cleanup() {
  trap - EXIT INT TERM
  [ ${#pids[@]} -eq 0 ] || kill "${pids[@]}" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$run"
}
trap cleanup EXIT INT TERM

gate4ai daemon --ui "127.0.0.1:$port" --keystore file &
pids+=($!)
for _ in $(seq 1 100); do
  gate4ai daemon status >/dev/null 2>&1 && break
  sleep 0.2
done
gate4ai daemon status >/dev/null || { echo "stand: the daemon did not start" >&2; exit 1; }

for c in connectors/*/; do
  [ -f "$c/manifest.yaml" ] || continue
  gate4ai connectors install "${c%/}" --yes --watch &
  pids+=($!)
done

if [ "${STAND_PUBLIC:-}" = 1 ]; then
  gate4ai relay status >/dev/null 2>&1 || gate4ai relay register --url https://relay.gate4.ai
  gate4ai relay status
fi

echo "stand: data $stand, sockets $run"
echo "stand: CLI against the stand: GATE4AI_HOME=$GATE4AI_HOME GATE4AI_SOCKET=$GATE4AI_SOCKET GATE4AI_AGENT_SOCKET=$GATE4AI_AGENT_SOCKET $stand/bin/gate4ai ..."
echo "stand: a new login link: the same with 'ui --print' (each link works once, for 2 minutes)"
echo "stand: Web UI $(gate4ai ui --print 2>/dev/null)"
wait
