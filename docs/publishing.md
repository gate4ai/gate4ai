# Выпуск и публикация на github.com/gate4ai

С 2026-10-21 код продукта — один репозиторий `gate4ai/gate4ai` и один Go-модуль `github.com/gate4ai/gate4ai`. Прежние отдельные репозитории (`sdk-go`, `relay`, `gate4ai-mail`, `gate4ai-browser`, `gate4ai-files`, `e2e`) перенесены сюда без истории и удалены; обратной совместимости с их путями модулей и релизами нет.

## Репозитории

| Локальный каталог (в общем каталоге проекта, сам он не git) | Репозиторий | Видимость | Что |
|---|---|---|---|
| `gate4ai/` | `gate4ai/gate4ai` | публичный | ядро и трей, `sdk-go/`, `relay/`, `connectors/`, `e2e/` |
| `registry/` | `gate4ai/registry` | публичный | индекс магазина (CC0) и его подпись |
| `www/` | `gate4ai/www` | приватный | сайт gate4.ai; CI собирает образ `ghcr.io/gate4ai/www:<sha>` и выкладывает его на сервер relay |
| `dev/` | `gate4ai/dev` | приватный | скрипты процессов pitwall: `deploy.sh`, стенд, тесты |
| `.github/` | `gate4ai/.github` | публичный | профиль организации, CONTRIBUTING, SECURITY.md |
| `.secrets/keys/` | — | вне git | ключ издателя и ключ registry |

Лицензии — по каталогам (SPDX в каждом файле проверяет `scripts/spdx.sh`): `sdk-go/` — Apache-2.0 (свой `LICENSE`), остальное — AGPL-3.0-only (`LICENSE` в корне); индекс registry — CC0. Внешний вклад — с CLA и DCO (`Signed-off-by` в каждом коммите).

## Версии

Одна версия на всё: тег `vX.Y.Z` выпускает `gate4ai`, `gate4ai-tray`, `gate4ai-relay` и пакеты всех коннекторов. Версия коннектора в `manifest.yaml` — версия тега без `v` (`version: "0.7.0"` у тега `v0.7.0`); её выставляет `dev/scripts/deploy.sh tag` перед тегом. Выпущенную версию не перевыпускать: новая сборка — новая версия.

Сторонние коннекторы и сайт берут SDK из этого модуля: `require github.com/gate4ai/gate4ai vX.Y.Z`, импорт `github.com/gate4ai/gate4ai/sdk-go/...` (собирается только нужное: пакеты `sdk-go` зависят лишь от `sdk-go` и `gopkg.in/yaml.v3`).

## Релиз (`.github/workflows/release.yml`, тег `v*`)

1. Те же проверки, что в `ci.yml`: SPDX, направление зависимостей, сборка, vet, тесты, WASM, `contract diff` с прошлым тегом, сборка под macOS и Windows, сквозные тесты `e2e/`.
2. GoReleaser — черновик релиза: `gate4ai` и трей под три ОС, `gate4ai-relay` под Linux; `checksums.txt` с подписью cosign (keyless).
3. Трей под macOS (cgo, macOS-раннер) — в тот же черновик.
4. Пакеты коннекторов: `gate4ai connectors pack` из этого же коммита, подпись ключом издателя (`GATE4AI_PUBLISHER_KEY`); в релиз — `.g4c`, `.platforms.json` нативных и `<коннектор>.pack.json`. Сборка воспроизводимая (Go из `go.mod`, без сведений о git): нативный пакет — по `.g4c` на платформу из `manifest.platforms` (`CGO_ENABLED=0 -trimpath`) и список платформ, на который ссылается запись индекса.
5. Публикация черновика (`--latest`): только теперь его видят `https://gate4.ai/latest` и установщики.
6. `gate4ai-relay` из релиза — на сервер relay.gate4.ai (окружение `production`: `vars.DEPLOY_HOST`, `secrets.DEPLOY_SSH_KEY`, `secrets.DEPLOY_KNOWN_HOSTS`; без них шаг пропускается). Скрипт на сервере (`relay/deploy/remote-install.sh`) меняет только бинарник и откатывает его, если API relay не ответил.

## Запись в registry

После релиза `dev/scripts/deploy.sh registry` скачивает все `*.pack.json` релиза, добавляет версии в `index.json` (`gate4ai registry add … --publisher gate4ai`; у нового коннектора `--repo https://github.com/gate4ai/gate4ai --path connectors/<каталог>`), подписывает индекс ключом registry (`gate4ai registry sign`, ключ — вне GitHub) и открывает один pull request на релиз. CI registry скачивает и сверяет каждый пакет, а новые версии пересобирает из тега в каталоге `path` и сравнивает с релизом. После зелёного CI — слияние. Сторонние издатели проходят тот же путь без подписи индекса: [CONTRIBUTING.md registry](https://github.com/gate4ai/registry/blob/main/CONTRIBUTING.md).

## Настройки организации

- **CLA Assistant и DCO** (GitHub App) на `gate4ai/gate4ai`, `registry` и `.github`.
- **Branch protection** на `main`: обязательный CI.
- **Секреты:** `GATE4AI_PUBLISHER_KEY` (организация) — приватный ключ издателя (`gate4ai connectors keygen`); окружение `production` в `gate4ai/gate4ai` — доступ к серверу relay; `REGISTRY_PUBLIC_KEY` — переменная `registry` для CI.
