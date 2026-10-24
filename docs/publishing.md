# Выпуск и публикация на github.com/gate4ai

Код продукта — один репозиторий `gate4ai/gate4ai` и один Go-модуль `github.com/gate4ai/gate4ai`. Архивные клоны прежних отдельных репозиториев в каталоге проекта не выпускаются, не деплоятся и не тестируются ([repo-structure.md](repo-structure.md#архивные-клоны)).

## Репозитории

Репозитории проекта (`gate4ai`, `registry`, `www`, `dev`, `.github`), их видимость, лицензии и зависимости — [repo-structure.md](repo-structure.md). Лицензии — по каталогам (SPDX в каждом файле проверяет `scripts/spdx.sh`): `sdk-go/` — Apache-2.0 (свой `LICENSE`), остальное — AGPL-3.0-only (`LICENSE` в корне); индекс registry — CC0. Внешний вклад — с CLA и DCO (`Signed-off-by` в каждом коммите).

## Версии

Одна версия на всё: тег `vX.Y.Z` выпускает `gate4ai`, `gate4ai-tray` и `gate4ai-relay`. Пакетов коннекторов в релизе нет: Браузер и Файлы встроены в бинарник `gate4ai` (`internal/connectors/builtin/`), их манифесты — часть ядра (версия в `manifest.yaml` браузера — версия тега без `v`). Контракт действий браузера для других коннекторов (`services`) сверяется с прошлым тегом (`gate4ai contract diff` по `internal/connectors/builtin/*/manifest.yaml`, шаг `contract` в `ci.yml`): несовместимое изменение без повышения `contract` — красный CI. Выпущенную версию не перевыпускать: новая сборка — новая версия.

Сторонние коннекторы и сайт берут SDK из этого модуля: `require github.com/gate4ai/gate4ai vX.Y.Z`, импорт `github.com/gate4ai/gate4ai/sdk-go/...` (собирается только нужное: пакеты `sdk-go` зависят лишь от `sdk-go` и `gopkg.in/yaml.v3`).

## Релиз (`.github/workflows/release.yml`, тег `v*`)

1. Те же проверки, что в `ci.yml` (`workflow_call`): SPDX, направление зависимостей, сборка, vet, тесты, WASM-сборка `sdk-go`, `contract diff` с прошлым тегом, сборка под macOS и Windows, сквозные тесты `e2e/`.
2. GoReleaser — черновик релиза: `gate4ai` и трей под три ОС, `gate4ai-relay` под Linux; `checksums.txt` с подписью cosign (keyless).
3. Трей под macOS (cgo, macOS-раннер) — в тот же черновик.
4. Публикация черновика (`--latest`): только теперь его видят `https://gate4.ai/latest` и установщики.
5. `gate4ai-relay` из релиза — на сервер relay.gate4.ai (окружение `production`: `vars.DEPLOY_HOST`, `secrets.DEPLOY_SSH_KEY`, `secrets.DEPLOY_KNOWN_HOSTS`; без них шаг пропускается). Бинарник загружается в каталог, созданный `mktemp -d` на сервере (не в предсказуемый путь `/tmp`); скрипт на сервере (`relay/deploy/remote-install.sh <путь>`) меняет только бинарник и откатывает его, если API relay не ответил.

Сторонние actions во всех workflow закреплены по полному SHA (тег — в комментарии), обновления предлагает Dependabot (`.github/dependabot.yml`): в job'ах релиза есть `contents: write` и `id-token: write` (cosign). Подпись `checksums.txt` установщик проверяет по идентичности именно этого workflow на теге (`…/.github/workflows/release.yml@refs/tags/v…`).

## Запись в registry

Официальных коннекторов в магазине нет: Браузер и Файлы — часть `gate4ai`, релиз в registry не записывается. Индекс (`index.json`) содержит только коннекторы издателей (WASM): издатель собирает пакет `gate4ai connectors pack`, подписывает его своим ключом (`gate4ai connectors keygen`) и присылает `<коннектор>.pack.json` в pull request registry (`gate4ai registry add … --publisher <имя> --repo <репозиторий> --path <каталог>`); CI registry ставит gate4ai версии из `.github/gate4ai-version`, скачивает и сверяет каждый пакет с его записью и пересобирает новые версии из исходников; индекс подписывает ключ registry (`gate4ai registry sign`, ключ — вне GitHub). Порядок — [CONTRIBUTING.md registry](https://github.com/gate4ai/registry/blob/main/CONTRIBUTING.md). Старые установки при старте новой версии удаляют поставленные из магазина `browser` и `email` (`internal/daemon/migrate.go`). Контракт 1 действий браузера считается от схем v0.7.7.

## Настройки организации

- **CLA Assistant и DCO** (GitHub App) на `gate4ai/gate4ai`, `registry` и `.github`.
- **Branch protection** на `main`: обязательный CI.
- **Секреты:** окружение `production` в `gate4ai/gate4ai` — доступ к серверу relay; `REGISTRY_PUBLIC_KEY` — переменная `registry` для CI. Ключ издателя gate4ai (`GATE4AI_PUBLISHER_KEY`) релизу больше не нужен: пакетов коннекторов он не собирает.
