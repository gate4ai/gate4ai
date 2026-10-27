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

6. **Attestations и образ.** Каждый архив и `checksums.txt` получают GitHub artifact attestation (SLSA provenance, `actions/attest-build-provenance`): проверка — `gh attestation verify <архив> -R gate4ai/gate4ai`. Job `docker` собирает `Dockerfile` (gate4ai + Chrome, Xvfb, шрифты; linux/amd64 и arm64) в `ghcr.io/gate4ai/gate4ai:<версия>` и `:latest`, подписывает образ cosign и прикладывает attestation; публикация релиза ждёт и его.
7. **Каталоги.** GoReleaser готовит cask Homebrew (`dist/homebrew`) и манифест winget (`dist/winget`) всегда; в репозиторий каталога они уходят только при токене: `secrets.HOMEBREW_TAP_TOKEN` (PAT с правом записи в `gate4ai/homebrew-tap`, формула `Formula/gate4ai.rb` → `brew install gate4ai/tap/gate4ai`) и `secrets.WINGET_TOKEN` (PAT для форка `gate4ai/winget-pkgs`, pull request в `microsoft/winget-pkgs`; идентификатор `gate4ai.gate4ai`). Без токенов шаг пропускается (`skip_upload`). AUR и скилл в ClawHub — руками по чеклисту `dev/docs/catalogs.md`.

Workflow идёт один на тег (`concurrency: release-<ref>`, `cancel-in-progress`): GitHub иногда регистрирует один push как два события и запускает два релиза одновременно (так было на v0.1.1 и v0.7.20 — второй прогон падал в `tray-darwin` на уже загруженных ассетах); теперь второй прогон отменяет первый, а загрузка трея идёт с `--clobber`, так что и повтор упавшего job после частичной загрузки проходит. `dev/scripts/deploy.sh tag` ждёт самый новый прогон тега и переключается на него, если тот, за которым следил, отменён дублем.

Сторонние actions во всех workflow закреплены по полному SHA (тег — в комментарии), обновления предлагает Dependabot (`.github/dependabot.yml`): в job'ах релиза есть `contents: write` и `id-token: write` (cosign). Подпись `checksums.txt` установщик проверяет по идентичности именно этого workflow на теге (`…/.github/workflows/release.yml@refs/tags/v…`).

## Запись в registry

Официальных коннекторов в магазине нет: Браузер и Файлы — часть `gate4ai`, релиз в registry не записывается. Индекс (`index.json`) содержит только коннекторы издателей (WASM): издатель собирает пакет `gate4ai connectors pack`, подписывает его своим ключом (`gate4ai connectors keygen`) и присылает `<коннектор>.pack.json` в pull request registry (`gate4ai registry add … --publisher <имя> --repo <репозиторий> --path <каталог>`); CI registry ставит gate4ai версии из `.github/gate4ai-version`, скачивает и сверяет каждый пакет с его записью и пересобирает новые версии из исходников; индекс подписывает ключ registry (`gate4ai registry sign`, ключ — вне GitHub). Порядок — [CONTRIBUTING.md registry](https://github.com/gate4ai/registry/blob/main/CONTRIBUTING.md). Старые установки при старте новой версии удаляют поставленные из магазина `browser` и `email` (`internal/daemon/migrate.go`). Контракт 1 действий браузера считается от схем v0.7.7.

## Сигналы доверия

Проект молодой, поэтому доверие даёт проверяемость, а не возраст: подпись cosign и attestations релизов, подписанный образ, воспроизводимая сборка (`-trimpath`, `CGO_ENABLED=0`, `mod_timestamp` коммита), `SECURITY.md` с политикой раскрытия, workflow `scorecard.yml` (OpenSSF Scorecard, бейдж в README), `codeql.yml` (SAST: Go, JavaScript, workflow), actions по SHA и Dependabot. Ruleset `main`: без удаления и force-push, линейная история, обязательные проверки `test`, `e2e`, `build (macos-latest)`, `build (windows-latest)` из `ci.yml`; администраторы организации обходят правила (так `deploy.sh` пушит слитый коммит в `main`). Что настроить руками в организации GitHub (верификация домена gate4.ai, профиль, topics, Discussions) и где публиковаться — чеклисты `dev/docs/github-org.md` и `dev/docs/launch.md`.

## Настройки организации

- **CLA Assistant и DCO** (GitHub App) на `gate4ai/gate4ai`, `registry` и `.github`.
- **Branch protection** на `main`: обязательный CI.
- **Scorecard:** workflow `scorecard.yml` публикует результаты (`publish_results: true`) — для этого репозиторий должен быть публичным, а в Settings → Code security включён code scanning (SARIF).
- **Секреты:** окружение `production` в `gate4ai/gate4ai` — доступ к серверу relay; `REGISTRY_PUBLIC_KEY` — переменная `registry` для CI. Ключ издателя gate4ai (`GATE4AI_PUBLISHER_KEY`) релизу больше не нужен: пакетов коннекторов он не собирает.
