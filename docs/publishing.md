# Публикация на github.com/gate4ai

Монорепозиторий делится на репозитории скриптом; публикует человек. Порядок важен: модули, от которых зависят другие, публикуются первыми.

## 1. Разделить и проверить

```sh
./scripts/secret-scan.sh            # секреты во всей истории (и gitleaks, если установлен)
./scripts/split.sh ../gate4ai-split # репозитории с историей + проверка сборки и тестов каждого отдельно
```

| Каталог монорепозитория | Репозиторий | Лицензия |
|---|---|---|
| (корень без выделенных каталогов) | `gate4ai/gate4ai` | FSL-1.1-ALv2 |
| `sdk-go/` | `gate4ai/sdk-go` | Apache-2.0 |
| `connectors/mail/`, `files/`, `browser/` | `gate4ai/gate4ai-mail`, `gate4ai-files`, `gate4ai-browser` | AGPL-3.0-only + исключение для хоста |
| `relay/` | `gate4ai/relay` | FSL-1.1-ALv2 |
| `registry/` | `gate4ai/registry` | CC0 |
| `github-org/` | `gate4ai/.github` | — |

В истории ядра LICENSE во всех коммитах заменён на FSL-1.1-ALv2 (ранние коммиты были под AGPL, но не публиковались).

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

- **CLA Assistant** для `gate4ai`, `relay` (текст CLA — от юриста); **DCO** (GitHub App) для `sdk-go`, коннекторов, `registry`, `e2e`.
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
| `connectors/mail/`, `connectors/files/`, `connectors/browser/` | `gate4ai/gate4ai-mail`, `gate4ai-files`, `gate4ai-browser` |
| `www/` | `gate4ai/www` — сайт gate4.ai (Rust), **приватный**, создан 2026-10-14; деплой на сервер relay из CI |
| `e2e/` | `gate4ai/e2e` — сквозные тесты (AGPL-3.0-only), создан 2026-10-14, ещё не опубликован |
| `connectors/auth/` | будущие auth-модули |
| `.secrets/keys/` | ключ издателя и ключ registry; вне git |

## 6. Выпуск версии коннектора

1. `version` в `manifest.yaml` +1, коммит, тег `v<version>` (`version: "3"` → `v3`). CI собирает модуль воспроизводимо (Go из `go.mod`, без сведений о git), подписывает пакет ключом издателя и создаёт релиз с `<connector>-<version>.g4c` и `pack.json`. Выпущенную версию не перевыпускать: новая сборка — новая версия.
2. В ветке `registry`: `gate4ai registry add index.json pack.json --publisher gate4ai`, затем `gate4ai registry sign index.json --key ../.secrets/keys/registry.key`; pull request. CI скачивает и сверяет каждый пакет, а новые версии пересобирает из тега и сравнивает с релизом.
3. После ревью — слияние. Сторонние издатели проходят тот же путь без подписи индекса: [CONTRIBUTING.md registry](https://github.com/gate4ai/registry/blob/main/CONTRIBUTING.md).
