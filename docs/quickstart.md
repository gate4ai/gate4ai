# gate4.ai — запуск прототипа

Спецификация продукта — [README.md](../README.md), план и принятые допущения — [PLAN.md](../PLAN.md).

## Сборка

Нужен Go 1.27+. Всё на чистом Go (SQLite — `modernc.org/sqlite`), кросс-сборка под macOS, Linux и Windows:

```sh
make bin             # bin/gate4ai, bin/gate4ai-relay
make connectors      # bin/connectors/{mail,files,browser}.g4c — пакеты официальных коннекторов
./scripts/ci.sh      # по всем модулям workspace: build / vet / test + кросс-сборка darwin и windows
```

Репозиторий — Go workspace (`go.work`): ядро и будущие отдельные репозитории (`relay/`, `sdk-go/`, `connectors/*`) — отдельные модули, см. [repo-structure.md](../repo-structure.md).

Положите `gate4ai` в `PATH`.

| Бинарник | Что это |
|---|---|
| `gate4ai` | единственный бинарник ядра: `gate4ai daemon` — политики, подтверждения, секреты, журнал, коннекторы, Web UI, Egress Proxy, туннель к relay; `gate4ai mcp` — MCP-сервер на stdio; остальные команды — CLI для агентов, скриптов и человека |
| `gate4ai-relay` | relay (необязателен — нужен, только если нет своего HTTPS до daemon): публичный адрес устройства, TLS-passthrough, DNS для ACME, свидетель журнала |
| `gate4ai-tray` | трей (необязателен): статус daemon, уведомления о запросах подтверждения, «Разрешить / Отклонить», открытие Web UI. Ходит в сокет владельца, как CLI. На macOS собирается с cgo (`CGO_ENABLED=1 go build ./cmd/gate4ai-tray` на macOS) |

## 1. Daemon и коннекторы

```sh
gate4ai daemon &                 # первый запуск: пустая политика, агентов нет
gate4ai connectors install bin/connectors/mail.g4c --dev
gate4ai connectors install bin/connectors/files.g4c --dev
gate4ai connectors install bin/connectors/browser.g4c --dev
gate4ai connectors list
```

Коннекторы — WASM-модули: всё, что им можно, объявлено в манифесте (`capabilities`) и показывается при установке; без согласия владельца коннектор не ставится, а обновление с новыми возможностями требует согласия заново. `--dev` — пакет собственной сборки без подписи доверенного издателя. Из магазина: `gate4ai connectors install email` (нужны `store.registry` и `store.registry_keys` в конфигурации), `gate4ai connectors update <имя>`, `gate4ai connectors refresh` — список отозванных версий.

## 2. Конфигурация

Правила ссылаются на действия установленных коннекторов, поэтому политику задают после установки. Минимальный `config.yaml` (полная модель — раздел «Политики» README):

```yaml
agents:
  claude-ai:   { profile: assistant }
  claude-code: { profile: dev }
lists:
  contacts_trusted: ["accountant@example.com"]
profiles:
  assistant:
    connectors: [email, files, browser]
    rules:
      - id: email-send-trusted
        action: email.send
        when: to.all(r, r in lists.contacts_trusted)
        effect: allow
      - id: email-no-untrusted-recipients
        action: email.send
        when: "'untrusted_content' in provenance.to"
        effect: deny
        reason: "Адресат взят из входящего письма"
      - id: obsidian-append
        action: files.append
        when: glob(path, "~/Documents/obsidian/**")
        effect: allow
    limits:
      - { id: mail-per-day, action: email.send, per_day: 20 }
  dev:
    connectors: [files]
    sandbox:
      allow_domains: [api.anthropic.com, registry.npmjs.org, github.com]
      ask_domains: ["*.githubusercontent.com"]
connectors:
  email:
    address: me@example.com
    imap: { host: imap.gmail.com, username: me@example.com, password: "@mail.work" }
    smtp: { host: smtp.gmail.com, port: 465 }
  files:
    roots: ["~/Documents/obsidian"]
  browser:
    allowed_domains: ["*.gosuslugi.ru", "lk.mosenergosbyt.ru"]
browser:                      # браузерный воркер — служба хоста для коннекторов
  engine: stagehand
  model: openai/gpt-5.4-mini
  model_key: "@llm.key"
egress:
  secrets:
    - { domain: api.anthropic.com, header: X-Api-Key, placeholder: GATE4AI_PLACEHOLDER, secret: "@anthropic.key" }
approvals:
  channels: [desktop, telegram]
  timeout: 30m
  on_timeout: deny
  telegram: { token: "@telegram.bot", owner_id: 123456789 }
```

Строки `@имя` — алиасы секретов: сами секреты в конфигурации не хранятся, и коннекторы их не видят. Коннектор получает настройки с алиасами, а значение подставляет хост — при входе в IMAP/SMTP, в заголовке HTTP-запроса — и только для адресов, к которым секрет привязан в манифесте коннектора (`capabilities.secrets`). Пароль вместо алиаса в настройках коннектора не принимается.

```sh
gate4ai policy set config.yaml -m "первая политика"
```

`gate4ai daemon --config config.yaml` при первом запуске сразу берёт эту конфигурацию — если коннекторы уже установлены.

Данные — в `~/.local/state/gate4ai` (Linux), `~/Library/Application Support/gate4ai` (macOS), `%LOCALAPPDATA%\gate4ai` (Windows); `GATE4AI_HOME` переопределяет. Ключи — в системном keychain; если его нет (headless Linux), `--keystore file`.

Секреты (значение — из stdin, в историю shell не попадает):

```sh
printf %s 'пароль-приложения' | gate4ai vault set mail.work
printf %s '123456:ABC…'       | gate4ai vault set telegram.bot
gate4ai policy set config.yaml -m "секреты добавлены"   # перечитать каналы
```

Проверка: `gate4ai doctor`.

## 3. Агенты

**С shell (CLI + skill):**

```sh
gate4ai agents token claude-ai            # токен показывается один раз
export GATE4AI_TOKEN=g4a_…
gate4ai tools                              # что доступно профилю
gate4ai mail send --to landlord@example.com --subject "Аренда" --body "…" --wait 10m
gate4ai skill > SKILL.md                   # навык для Claude Code / Hermes / OpenClaw
```

Коды выхода: `0` ok, `2` denied_by_policy, `3` pending_approval, `4` verification_failed, `5` неверные аргументы, `6` daemon недоступен.

**MCP по stdio** (Claude Code и др.): `{"mcpServers": {"gate4ai": {"command": "gate4ai", "args": ["mcp"], "env": {"GATE4AI_TOKEN": "g4a_…"}}}}`.

**Изолированный локальный агент:**

```sh
gate4ai run claude-code --workspace ~/projects/policy             # sandbox-runtime
gate4ai run claude-code --workspace ~/projects/policy --container # Docker / Podman
gate4ai run claude-code --workspace ~/projects/policy --microvm   # Docker Sandboxes
gate4ai run claude-code --workspace . --dry-run                    # показать команду
```

Каждый запуск получает свой токен и сокет, отзываемые по завершении; сеть — только через Egress Proxy (`gate4ai egress` — журнал соединений).

**Облачные агенты (Claude.ai, ChatGPT) через relay:**

```sh
gate4ai relay register --url https://relay.gate4.ai
gate4ai relay status                      # mcp_url: https://<device-id>.relay.gate4.ai/mcp
gate4ai agents connect claude-ai          # одноразовый код подключения
```

Добавьте `mcp_url` как custom connector и введите код на странице авторизации.

**Агент с shell на другом компьютере (CLI + skill):** те же команды, адрес устройства — в `GATE4AI_URL`:

```sh
export GATE4AI_TOKEN=g4a_…                          # gate4ai agents token <agent> на устройстве
export GATE4AI_URL=https://<device-id>.relay.gate4.ai
gate4ai tools
```

**Без relay** (свой сертификат или reverse proxy на этом компьютере):

```sh
gate4ai daemon --public :8443 --public-url https://gate.example.com --public-cert gate.crt --public-key gate.key
gate4ai daemon --public 127.0.0.1:8080 --public-url https://gate.example.com   # TLS снимает reverse proxy
```

Публичный адрес один на всё: `/mcp` — MCP, `/v1/…` — CLI агентов, `/ui/` — Web UI, `/telegram` — вебхук бота.

## 4. Подтверждения, Web UI, журнал

- Подтверждения: Telegram-бот (кнопки «Разрешить / Отклонить / Показать текст»), уведомления ОС, трей, Web UI, `gate4ai approvals approve <id>`.
- Web UI: `gate4ai ui` — откроет браузер по одноразовой ссылке (http://127.0.0.1:7431). С телефона: `gate4ai ui --remote` выдаёт одноразовую ссылку на `<публичный адрес>/ui/`; удалённо доступны подтверждения и журнал, политики, агенты и секреты меняются только на этом компьютере.
- Telegram вебхуком вместо polling: `approvals.telegram.webhook: true` (нужен публичный адрес).
- Журнал: `gate4ai log`, `gate4ai show '#a3f9c21e'`, `gate4ai journal verify`, `gate4ai export -o backup.tar.gz --passphrase-env PASS`, офлайн-проверка `gate4ai verify backup.tar.gz --pubkey <hex>`.
- Политики: `gate4ai policy test --agent claude-ai --action email.send --args '{"to":["x@y.z"],"subject":"s"}'`, `gate4ai policy versions`, `gate4ai policy rollback <n>`.

## 5. Свой relay

```sh
gate4ai-relay --domain relay.example.org --listen :443 --dns :53 --ipv4 203.0.113.7 \
  --cert relay.crt --key relay.key --state /var/lib/gate4ai-relay/state.json
```

Зона `relay.example.org` делегируется (NS) на этот сервер: relay отвечает A-записями для устройств и TXT для ACME DNS-01. Для разработки — `--self-signed` и в конфигурации устройства `relay: {ca_file: relay-ca.pem, self_signed: true}`.

## Тесты

`go test ./...` — модульные тесты всех пакетов, интеграционные тесты daemon по сокетам, OAuth-поток, relay с passthrough, Egress Proxy, WASM-хост на тестовом коннекторе `testdata/probe` (в том числе вход хостом в IMAP на заглушке). Поток из README на собранных бинарниках и настоящих коннекторах — в репозитории [gate4ai/e2e](https://github.com/gate4ai/e2e).
