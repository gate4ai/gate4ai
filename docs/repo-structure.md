# Репозитории проекта

Проект gate4ai — несколько репозиториев на github.com/gate4ai. Локально они лежат рядом в общем каталоге проекта, который сам не git-репозиторий (так pitwall создаёт набор worktree задачи сразу по всем клонам). Код продукта — один репозиторий `gate4ai/gate4ai` и один Go-модуль `github.com/gate4ai/gate4ai`.

## Репозитории

| Локальный каталог | Репозиторий | Видимость | Лицензия | Что |
|---|---|---|---|---|
| `gate4ai/` | `gate4ai/gate4ai` | публичный | AGPL-3.0-only, `sdk-go/` — Apache-2.0 | ядро и трей (встроенные Браузер и Файлы — `internal/connectors/builtin/`), `sdk-go/`, `relay/`, `e2e/` (каталоги — ниже) |
| `registry/` | `gate4ai/registry` | публичный | CC0 1.0 (индекс); у коннекторов — свои, поле `license` | индекс магазина `index.json` и его подпись; версия gate4ai для CI — `.github/gate4ai-version` |
| `www/` | `gate4ai/www` | публичный | — (без лицензии) | сайт gate4.ai и установщики `install.sh`/`install.ps1`; CI собирает образ `ghcr.io/gate4ai/www:<sha>` и выкладывает его на сервер relay |
| `dev/` | `gate4ai/dev` | приватный | — (не публикуется) | скрипты процессов pitwall: `deploy.sh`, стенд, тесты набора |
| `.github/` | `gate4ai/.github` | публичный | — | профиль организации, CONTRIBUTING, SECURITY.md |
| `.secrets/keys/` | — | вне git | — | ключ издателя и ключ registry |

Внешний вклад — с CLA и DCO (`Signed-off-by` в каждом коммите). SPDX-заголовок в каждом файле `gate4ai/` проверяет `scripts/spdx.sh`.

## Каталоги монорепозитория `gate4ai/`

| Каталог | Лицензия | Может импортировать |
|---|---|---|
| `cmd/`, `internal/` (ядро: daemon, CLI, трей; `internal/connectors/builtin/{browser,files}` — встроенные компоненты) | AGPL-3.0-only | ядро, `sdk-go`; `relay` — только в тестах |
| `sdk-go/` (манифест, ABI, PDK, формат индекса registry) | Apache-2.0 (свой `LICENSE`) | только `sdk-go` |
| `relay/` (сервер relay) | AGPL-3.0-only | `relay`, `sdk-go` |
| `e2e/` (сквозные тесты; `testsite` — тестовый сайт, поднимается локально; `testconnector` — тестовый WASM-коннектор к нему поверх браузера) | AGPL-3.0-only | что угодно; `e2e` не импортирует никто |

Направление зависимостей проверяет `scripts/deps-check.sh` (в CI — `scripts/ci.sh`); какой компонент спецификации в каком пакете — [architecture.md](architecture.md).

## Зависимости между репозиториями

- `www` требует модуль `github.com/gate4ai/gate4ai` выпущенной версии (`require … vX.Y.Z`; берёт `sdk-go/registry` для проверки подписи индекса). Обновляет её `dev/scripts/deploy.sh gate4ai-bump`. Локально `dev/scripts/test-set.sh` собирает сайт с соседним `gate4ai/` через временный `go.work`.
- `registry` — индекс коннекторов сторонних издателей (WASM); официальных коннекторов в нём нет: Браузер и Файлы встроены в `gate4ai`. CI registry ставит gate4ai версии из `.github/gate4ai-version` ([publishing.md](publishing.md)).
- Сторонние коннекторы требуют `github.com/gate4ai/gate4ai` и импортируют только `sdk-go/...`.
- `dev` работает со всеми клонами сразу и ничего не импортирует.

## Архивные клоны

Клоны `relay/`, `sdk-go/`, `e2e/`, `connectors/mail/`, `connectors/browser/` в корне каталога проекта (если есть) — архив прежних отдельных репозиториев; код relay, SDK и e2e — в `gate4ai/`, браузер — встроен в ядро (`internal/connectors/builtin/browser`), почты больше нет: в README каждого есть пометка, деплоя и релизов в их workflow нет, а `dev/scripts` их не тестирует и не включает в `go.work` (список репозиториев — `project_repos` в `dev/scripts/workset.sh`). Правки — только в `gate4ai/`; сами клоны можно удалить.
