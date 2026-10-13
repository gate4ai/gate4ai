#!/usr/bin/env bash
# Поиск секретов во всей истории git перед публикацией: приватные ключи,
# токены облаков, GitHub, Slack, Telegram, OpenAI/Anthropic. Тестовые фейки
# секретами не являются и шаблонам не соответствуют. Для полной проверки —
# gitleaks (`gitleaks git --redact`), если установлен.
#   scripts/secret-scan.sh [repo-dir]
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"
patterns=(
  '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----'
  'ed25519-private:[A-Za-z0-9+/]{40,}'
  'AKIA[0-9A-Z]{16}'
  'gh[pousr]_[A-Za-z0-9]{36,}'
  'github_pat_[A-Za-z0-9_]{40,}'
  'xox[abposr]-[A-Za-z0-9-]{10,}'
  'AIza[0-9A-Za-z_-]{35}'
  'sk-(ant-|proj-)?[A-Za-z0-9_-]{32,}'
  '[0-9]{8,10}:AA[A-Za-z0-9_-]{33}'
)
found=0
for p in "${patterns[@]}"; do
  if hits=$(git log --all -p --no-color | grep -E -n -- "^\+.*($p)" | head -5) && [ -n "$hits" ]; then
    echo "possible secret ($p):"
    echo "$hits" | cut -c1-160
    found=1
  fi
done
if command -v gitleaks >/dev/null; then
  gitleaks git --redact --no-banner . || found=1
fi
if [ "$found" = 1 ]; then
  echo "secret-scan: review the hits above before publishing" >&2
  exit 1
fi
echo "secret-scan: ok ($(git rev-list --all | wc -l) commits)"
