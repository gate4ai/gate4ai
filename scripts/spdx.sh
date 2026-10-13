#!/usr/bin/env bash
# SPDX-заголовки исходников: лицензия определяется каталогом.
#   scripts/spdx.sh         — добавить недостающие заголовки
#   scripts/spdx.sh --check — только проверить (CI)
set -euo pipefail
cd "$(dirname "$0")/.."
check=${1:-}
license_for() {
  case "$1" in
    connectors/*) echo "AGPL-3.0-only" ;;
    sdk-go/*|registry/*) echo "Apache-2.0" ;;
    *) echo "FSL-1.1-ALv2" ;;
  esac
}
bad=0
while IFS= read -r f; do
  [ -f "$f" ] || continue
  want="// SPDX-License-Identifier: $(license_for "$f")"
  first=$(head -n1 "$f")
  [ "$first" = "$want" ] && continue
  if [ "$check" = "--check" ]; then
    echo "missing or wrong SPDX header: $f"; bad=1; continue
  fi
  if [[ "$first" == "// SPDX-License-Identifier:"* ]]; then
    tail -n +2 "$f" > "$f.spdx"
  else
    cp "$f" "$f.spdx"
  fi
  # Пустая строка после заголовка: иначе он станет doc-комментарием пакета.
  { echo "$want"; [ "$(head -n1 "$f.spdx")" = "" ] || echo; cat "$f.spdx"; } > "$f" && rm "$f.spdx"
done < <(git ls-files --cached --others --exclude-standard -- '*.go' '*.rs' | grep -v '^.*/testdata/')
exit $bad
