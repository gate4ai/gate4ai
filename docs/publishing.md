# Публикация на github.com/gate4ai

Монорепозиторий делится на репозитории скриптом; публикует человек. Порядок важен: модули, от которых зависят другие, публикуются первыми.

## 1. Разделить и проверить

```sh
./scripts/secret-scan.sh            # секреты во всей истории (и gitleaks, если установлен)
./scripts/split.sh ../gate4ai-split # репозитории с историей + проверка сборки и тестов каждого отдельно
```

| Каталог монорепозитория | Репозиторий | Лицензия |
|---|---|---|
| (корень без выделенных каталогов) | `gate4ai/gate4ai` | AGPL-3.0-only |
| `sdk-go/` | `gate4ai/sdk-go` | Apache-2.0 |
| `connectors/mail/`, `files/`, `browser/` | `gate4ai/gate4ai-mail`, `gate4ai-files`, `gate4ai-browser` | AGPL-3.0-only |
| `relay/` | `gate4ai/relay` | AGPL-3.0-only |
| `registry/` | `gate4ai/registry` | CC0 |
| `github-org/` | `gate4ai/.github` | — |

Лицензии сменены 2026-10-17: ядро и relay — с FSL-1.1-ALv2 на AGPL-3.0-only, у коннекторов снято исключение для хоста (хост теперь сам под AGPL). Уже опубликованные версии остаются под прежними лицензиями. `split.sh` записывает в историю ядра текущий LICENSE.

## 2. Создать репозитории и отправить

```sh
cd ../gate4ai-split
for r in sdk-go relay gate4ai-mail gate4ai-files gate4ai-browser gate4ai registry .github; do
  gh repo create "gate4ai/$r" --public --source "$r" --push
done
```

## 3. Версии модулей и go.mod

До публикации модули ссылаются друг на друга через `go.work`, а в `go.mod` этих зависимостей нет. После публикации:

```sh
(cd sdk-go && git tag v0.1.0 && git push --tags)
(cd relay  && git tag v0.1.0 && git push --tags)
for r in gate4ai-mail gate4ai-files gate4ai-browser; do
  (cd $r && go get github.com/gate4ai/sdk-go@v0.1.0 && go mod tidy && git commit -am "require sdk-go v0.1.0" && git push)
done
(cd gate4ai && go get github.com/gate4ai/sdk-go@v0.1.0 github.com/gate4ai/relay@v0.1.0 && go mod tidy && git commit -am "require sdk-go, relay" && git push)
```

Сквозные тесты на настоящих коннекторах — в репозитории `gate4ai/e2e`: он собирает соседние исходники через свой `go.work`, а `ci.yml` каждого ключевого репозитория (`gate4ai`, `sdk-go`, `relay`, коннекторы) вызывает его workflow со своим коммитом. Хост от коннекторов не зависит даже в тестах.

## 4. Настройки организации

- **CLA Assistant** для AGPL-репозиториев: `gate4ai`, `relay`, коннекторов, `e2e` (текст CLA — от юриста; сохраняет право выпускать версии под другой лицензией); **DCO** (GitHub App) для `sdk-go`, `registry`.
- **Branch protection** на `main`: обязательный CI, ревью.
- **Секреты:** `GATE4AI_PUBLISHER_KEY` (организация, для коннекторов) — приватный ключ издателя (`gate4ai connectors keygen`); ключ registry — вне GitHub, подпись индекса — вручную (`gate4ai registry sign`); `REGISTRY_PUBLIC_KEY` — переменная `registry` для CI.
- **Релизы:** тег `v*` в `gate4ai` и `relay` — GoReleaser со сборкой под три ОС и подписью cosign (keyless); в коннекторах — пакет `.g4c`, подписанный ключом издателя, затем pull request в `registry`.

## 5. После публикации: рабочие копии

Репозитории уже опубликованы без истории (один начальный коммит), поэтому `split.sh` для них больше не запускается: он пересоздал бы историю. Изменения — обычными коммитами.

Монорепозитория больше нет (удалён 2026-10-14, история не сохранялась); разделы 1–3 описывают, как репозитории были созданы. Правки делаются прямо в клонах в общем каталоге `gate4ai/`:

| Каталог | Репозиторий |
|---|---|
| `gate4ai/` | `gate4ai/gate4ai` |
| `sdk-go/`, `relay/`, `registry/`, `.github/` | одноимённые `gate4ai/*` |
| — | `gate4ai/tray` удалён 2026-10-14: трей теперь `cmd/gate4ai-tray` в `gate4ai/gate4ai` |
| `connectors/mail/`, `connectors/browser/` | `gate4ai/gate4ai-mail`, `gate4ai-browser` |
| — | `gate4ai/gate4ai-files` — в архиве, локальный клон удалён 2026-10-18: с v0.6 ядра файлы — встроенный коннектор (`internal/connectors/builtin/files`); запись в registry убрана 2026-10-18 |
| — | `connectors/tbank/` (без репозитория) удалён 2026-10-18 по решению человека: коннектор будет сделан заново |
| `www/` | `gate4ai/www` — сайт gate4.ai (Rust), **приватный**, создан 2026-10-14; деплой на сервер relay из CI |
| `e2e/` | `gate4ai/e2e` — сквозные тесты (AGPL-3.0-only), создан 2026-10-14, ещё не опубликован |
| `connectors/auth/` | auth-модули (пока только [README](../../connectors/auth/README.md), вне git) |
| `.secrets/keys/` | ключ издателя и ключ registry; вне git |

## 6. Выпуск версии коннектора

1. `version` в `manifest.yaml` +1, коммит, тег `v<version>` (`version: "3"` → `v3`). CI собирает модуль воспроизводимо (Go из `go.mod`, без сведений о git), подписывает пакет ключом издателя и создаёт релиз с `<connector>-<version>.g4c` и `pack.json`. Выпущенную версию не перевыпускать: новая сборка — новая версия.
   Нативный пакет (`runtime: native`, сейчас `browser`): `gate4ai connectors pack` собирает бинарник под каждую платформу из `manifest.platforms` (`CGO_ENABLED=0 -trimpath`), по `.g4c` на платформу, и список `<connector>-<version>.platforms.json`; в релизе — все `.g4c`, список и `pack.json`, в индексе registry запись ссылается на список (так индекс читают и прежние версии ядра). У поставщика действий для других коннекторов (`services`) CI сверяет манифест с прошлым тегом: `gate4ai contract diff` — несовместимое изменение без нового `contract` не выпускается.
2. В ветке `registry`: `gate4ai registry add index.json pack.json --publisher gate4ai`, затем `gate4ai registry sign index.json --key ../.secrets/keys/registry.key`; pull request. CI скачивает и сверяет каждый пакет, а новые версии пересобирает из тега и сравнивает с релизом.
3. После ревью — слияние. Сторонние издатели проходят тот же путь без подписи индекса: [CONTRIBUTING.md registry](https://github.com/gate4ai/registry/blob/main/CONTRIBUTING.md).
