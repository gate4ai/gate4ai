# Коннектор «Почта (IMAP/SMTP)» для gate4.ai

Чтение писем, черновики и отправка с проверкой по «Отправленным». Соединения открывает хост и сам выполняет вход по алиасу пароля из Vault — коннектор пароль не видит. Входящие письма помечаются как недоверенный контент; темы и тексты в других кодировках (windows-1251, koi8-r…) перекодируются в UTF-8, у HTML-писем отбрасываются стили и скрипты. `in_reply_to` — только Message-ID вида `<id@host>` (как его отдают `list` / `read`); значение заголовка с переводом строки — `bad_request`, письмо не уходит. При `sent_copy: client` ошибка сохранения копии в «Отправленные» — в поле `client_copy_error` ответа `send`.

Коннектор — WASM-модуль для хоста [gate4ai](https://github.com/gate4ai/gate4ai): что ему можно, объявлено в [manifest.yaml](manifest.yaml) (`capabilities`) и показывается владельцу при установке; всё остальное хост запрещает (`capability_denied`). Написан на [sdk-go](../../sdk-go).

## Установка

```sh
gate4ai connectors install email                 # из магазина gate4.ai
# или своя сборка:
gate4ai connectors pack . -o email.g4c && gate4ai connectors install email.g4c --dev
```

Настройки — `connectors.email` в конфигурации gate4ai (секреты — только алиасы Vault, `@имя`).

## Разработка

```sh
go test ./...                                                    # нативно, на тестовом хосте sdk-go/hosttest
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o connector.wasm .
gate4ai connectors install . --watch                             # поставить в свой daemon из исходников и пересобирать на каждое сохранение
```

## Лицензия

[AGPL-3.0-only](../../LICENSE). Хост gate4ai тоже под AGPL-3.0, так что коннектор исполняется в нём и распространяется вместе с ним без дополнительных разрешений. Вклад — по CLA и DCO.
