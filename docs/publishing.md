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
| `tray/` | `gate4ai/tray` | FSL-1.1-ALv2 |
| `registry/` | `gate4ai/registry` | CC0 |
| `github-org/` | `gate4ai/.github` | — |

В истории ядра LICENSE во всех коммитах заменён на FSL-1.1-ALv2 (ранние коммиты были под AGPL, но не публиковались).

## 2. Создать репозитории и отправить

```sh
cd ../gate4ai-split
for r in sdk-go relay gate4ai-mail gate4ai-files gate4ai-browser gate4ai tray registry .github; do
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

Тесты ядра, которые собирают коннекторы из исходников (`connectors/…`), в отдельном репозитории пропускаются; сквозную проверку на настоящих коннекторах продолжает делать монорепозиторий.

## 4. Настройки организации

- **CLA Assistant** для `gate4ai`, `relay`, `tray` (текст CLA — от юриста); **DCO** (GitHub App) для `sdk-go`, коннекторов, `registry`.
- **Branch protection** на `main`: обязательный CI, ревью.
- **Секреты:** `GATE4AI_PUBLISHER_KEY` (организация, для коннекторов) — приватный ключ издателя (`gate4ai connectors keygen`); ключ registry — вне GitHub, подпись индекса — вручную (`gate4ai registry sign`); `REGISTRY_PUBLIC_KEY` — переменная `registry` для CI.
- **Релизы:** тег `v*` в `gate4ai` и `relay` — GoReleaser со сборкой под три ОС и подписью cosign (keyless); в коннекторах — пакет `.g4c`, подписанный ключом издателя, затем pull request в `registry`.
