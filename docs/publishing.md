# Выпуск и публикация на github.com/gate4ai

Код продукта — один репозиторий `gate4ai/gate4ai` и один Go-модуль `github.com/gate4ai/gate4ai`. Архивные клоны прежних отдельных репозиториев в каталоге проекта не выпускаются, не деплоятся и не тестируются ([repo-structure.md](repo-structure.md#архивные-клоны)).

## Репозитории

Репозитории проекта (`gate4ai`, `registry`, `www`, `dev`, `.github`), их видимость, лицензии и зависимости — [repo-structure.md](repo-structure.md). Лицензии — по каталогам (SPDX в каждом файле проверяет `scripts/spdx.sh`): `sdk-go/` — Apache-2.0 (свой `LICENSE`), остальное — AGPL-3.0-only (`LICENSE` в корне); индекс registry — CC0. Внешний вклад — с CLA и DCO (`Signed-off-by` в каждом коммите).

## Версии

Одна версия на всё: тег `vX.Y.Z` выпускает `gate4ai`, `gate4ai-tray`, `gate4ai-relay` и пакеты всех коннекторов. Версия коннектора в `manifest.yaml` — версия тега без `v` (`version: "0.7.0"` у тега `v0.7.0`); её выставляет `dev/scripts/deploy.sh tag` перед тегом. Выпущенную версию не перевыпускать: новая сборка — новая версия.

Сторонние коннекторы и сайт берут SDK из этого модуля: `require github.com/gate4ai/gate4ai vX.Y.Z`, импорт `github.com/gate4ai/gate4ai/sdk-go/...` (собирается только нужное: пакеты `sdk-go` зависят лишь от `sdk-go` и `gopkg.in/yaml.v3`).

## Релиз (`.github/workflows/release.yml`, тег `v*`)

1. Те же проверки, что в `ci.yml`: SPDX, направление зависимостей, сборка, vet, тесты, WASM, `contract diff` с прошлым тегом, сборка под macOS и Windows, сквозные тесты `e2e/`.
2. GoReleaser — черновик релиза: `gate4ai` и трей под три ОС, `gate4ai-relay` под Linux; `checksums.txt` с подписью cosign (keyless).
3. Трей под macOS (cgo, macOS-раннер) — в тот же черновик.
4. Пакеты коннекторов: `gate4ai connectors pack` из этого же коммита, подпись ключом издателя (`GATE4AI_PUBLISHER_KEY`); в релиз — `.g4c`, `.platforms.json` нативных и `<коннектор>.pack.json`. Сборка воспроизводимая (Go из `go.mod`, без сведений о git): нативный пакет — по `.g4c` на платформу из `manifest.platforms` (`CGO_ENABLED=0 -trimpath`) и список платформ, на который ссылается запись индекса.
5. Публикация черновика (`--latest`): только теперь его видят `https://gate4.ai/latest` и установщики.
6. `gate4ai-relay` из релиза — на сервер relay.gate4.ai (окружение `production`: `vars.DEPLOY_HOST`, `secrets.DEPLOY_SSH_KEY`, `secrets.DEPLOY_KNOWN_HOSTS`; без них шаг пропускается). Бинарник загружается в каталог, созданный `mktemp -d` на сервере (не в предсказуемый путь `/tmp`); скрипт на сервере (`relay/deploy/remote-install.sh <путь>`) меняет только бинарник и откатывает его, если API relay не ответил.

Сторонние actions во всех workflow закреплены по полному SHA (тег — в комментарии), обновления предлагает Dependabot (`.github/dependabot.yml`): в job'ах релиза есть `contents: write`, `id-token: write` (cosign) и ключ издателя. Подпись `checksums.txt` установщик проверяет по идентичности именно этого workflow на теге (`…/.github/workflows/release.yml@refs/tags/v…`).

## Запись в registry

После релиза `dev/scripts/deploy.sh registry` скачивает все `*.pack.json` релиза, добавляет версии в `index.json` (`gate4ai registry add … --publisher gate4ai`; у нового коннектора `--repo https://github.com/gate4ai/gate4ai --path connectors/<каталог>`) gate4ai, собранным из исходников этого тега, записывает тег в `.github/gate4ai-version` registry, подписывает индекс ключом registry (`gate4ai registry sign`, ключ — вне GitHub) и открывает один pull request на релиз. CI registry ставит gate4ai версии из `.github/gate4ai-version` (не `@latest`), скачивает и сверяет каждый пакет, новые версии пересобирает из тега в каталоге `path` и сравнивает с релизом, а ещё проверяет, что официальные `email` и `browser` есть в индексе с этой версией (иначе CI красный: пустой магазин — не работают `connectors install email` и автоустановка browser; вышедший, но не записанный релиз — предупреждение). После зелёного CI — слияние. Релиз не закончен, пока `dev/scripts/deploy.sh in-registry` не проходит. Индекс начат заново с v0.7.8: прежние версии `email` и `browser` из него убраны, а контракт 1 считается от схем v0.7.7 (несовместимое изменение `observe` с v0.7.6 выпущено без повышения `contract` — чужих коннекторов ещё не было). Сторонние издатели проходят тот же путь без подписи индекса: [CONTRIBUTING.md registry](https://github.com/gate4ai/registry/blob/main/CONTRIBUTING.md).

## Настройки организации

- **CLA Assistant и DCO** (GitHub App) на `gate4ai/gate4ai`, `registry` и `.github`.
- **Branch protection** на `main`: обязательный CI.
- **Секреты:** `GATE4AI_PUBLISHER_KEY` (организация) — приватный ключ издателя (`gate4ai connectors keygen`); окружение `production` в `gate4ai/gate4ai` — доступ к серверу relay; `REGISTRY_PUBLIC_KEY` — переменная `registry` для CI.
