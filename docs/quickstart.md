# gate4.ai — сборка и запуск

Спецификация продукта — [specification.md](specification.md), краткая инструкция — [README.md](../README.md), план и принятые допущения — [PLAN.md](../PLAN.md).

## Сборка

Нужен Go 1.27+. Всё на чистом Go (SQLite — `modernc.org/sqlite`), кросс-сборка под macOS, Linux и Windows:

```sh
make bin             # bin/gate4ai, bin/gate4ai-relay
make connectors      # bin/connectors/{mail,browser,tbank}.g4c — пакеты официальных коннекторов
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
gate4ai daemon start             # в фоне; первый запуск: агентов нет, files встроен, магазин подключён, browser ставится из него (автозапуск: gate4ai service install)
gate4ai connectors install bin/connectors/mail.g4c --dev
gate4ai connectors install bin/connectors/browser.g4c --dev
gate4ai connectors list
```

Файлы — встроенный коннектор `files` (часть ядра, из магазина не ставится): в начальной конфигурации он в профиле `default` с корнем `~` (`connectors.files.roots`) и правилом `ask` на каждое действие; пакет `files` из магазина, поставленный прежней версией, daemon при запуске удаляет, настройки и правила остаются. Первый запуск без `--config` сам ставит из магазина `browser` (проверенный gate4.ai, согласие на его возможности — без вопроса; без сети — повтор в фоне и при следующем запуске). Своя сборка (`--dev`, как выше) заменяет поставленную из магазина; поставленный владельцем коннектор daemon не трогает. С `--config` и на уже настроенной установке сам ничего не ставит.

Каждый установленный или обновлённый коннектор daemon добавляет в профиль `default` (если он есть) и пишет туда правило `ask` (`<коннектор>-<действие>-ask`) на каждое действие, для которого в этом профиле ещё нет правила, — в том числе на чтение, у которого по умолчанию `allow`; действия только для других коннекторов (`service_only`) — без правила. Правила владельца на эти действия остаются как есть.

Коннекторы магазина — WASM-модули: всё, что им можно, объявлено в манифесте (`capabilities`) и показывается при установке; без согласия владельца коннектор не ставится, а обновление с новыми возможностями требует согласия заново. Исключение — нативные пакеты (`runtime: native`, сейчас только `browser`): отдельный процесс под надзором daemon, без песочницы, с правами пользователя; ставятся только от gate4.ai (или как своя сборка с `--dev`), на экране согласия — предупреждение. Установленный прежней версией WASM-браузер daemon при запуске заменяет нативным из магазина; раздел `browser` конфигурации (до v0.6) переносится в `connectors.browser` новой версией политики.

**Вызовы между коннекторами.** Коннектор вызывает действия другого через `service_call` (поставщик перечисляет их в `services`, потребитель — в `capabilities.services` с мажором `contract` и `params`, например сайтами для браузера); каждое действие потребителя перечисляет свои подвызовы (`calls`), и хост пропускает из него только их, не выше его класса. Подвызовы видны в описании инструмента, запросе подтверждения и на экране согласия, поэтому разрешение действия покрывает их — повторно не спрашивается; правило `deny` на действие поставщика действует и во вложенном вызове. Типизированный клиент — `gate4ai contract gen <манифест поставщика> -o client.go`, совместимость нового манифеста — `gate4ai contract diff old.yaml new.yaml`. `--dev` — пакет собственной сборки без подписи доверенного издателя. Из магазина: `gate4ai connectors install email` (нужны `store.registry` и `store.registry_keys` в конфигурации), `gate4ai connectors update <имя>`, `gate4ai connectors refresh` — список отозванных версий.

**Разработка коннектора — прямо из папки с исходниками** (без `pack` и `.g4c`):

```sh
gate4ai connectors install ../connectors/tbank          # собрать в WASM и поставить (своя сборка, как --dev)
gate4ai connectors update tbank                         # пересобрать из той же папки после правок
gate4ai connectors install ../connectors/tbank --watch  # пересобирать и переставлять на каждое сохранение
```

Сборку делает CLI в вашем окружении (ваш Go, `go.work`, `GOFLAGS`), ошибки компиляции видны сразу; daemon записывает папку источником установки (`dir:<путь>` в `gate4ai connectors list`). `--watch` следит за `.go`, `go.mod`, `go.sum` и `manifest.yaml`; ошибка сборки печатается, наблюдение продолжается. Если изменились возможности в манифесте — согласие спрашивается заново (`--yes` — согласиться без вопроса).

## 2. Конфигурация

Правила ссылаются на действия установленных коннекторов, поэтому политику задают после установки. Минимальный `config.yaml` (полная модель — раздел «Политики» [спецификации](specification.md#политики)):

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
  browser:                    # нативный пакет: сайты агента, движок, модель для extract
    allowed_domains: ["*.example.com", "app.example.org"]
    engine: stagehand
    model: openai/gpt-5.4-mini
    model_key: "@llm.key"         # значение передаётся только пакету browser (capabilities.reveal)
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

Метрики, логи и трейсы relay (`--metrics`, `--otlp`), стек Grafana + Prometheus + Loki + Tempo на сервере relay и дашборд — [relay/docs/observability.md](https://github.com/gate4ai/relay/blob/main/docs/observability.md).

## Тесты

`go test ./...` — модульные тесты всех пакетов, интеграционные тесты daemon по сокетам, OAuth-поток, relay с passthrough, Egress Proxy, WASM-хост на тестовом коннекторе `testdata/probe` (в том числе вход хостом в IMAP на заглушке). Поток из спецификации на собранных бинарниках и настоящих коннекторах — в репозитории [gate4ai/e2e](https://github.com/gate4ai/e2e).

## Разработка из общего каталога репозиториев

Скрипты запускаются из каталога, где рядом лежат клоны `gate4ai/`, `sdk-go/`, `relay/`, `connectors/*`, `e2e/` (или из набора worktree задачи pitwall с той же раскладкой). Go-модули собираются вместе через временный `go.work` (`GOWORK`), так что правка `sdk-go` сразу видна ядру и коннекторам; в дереве ничего не остаётся.

```sh
PITWALL_REPOS="gate4ai connectors/mail" gate4ai/scripts/test-set.sh   # vet и тесты изменённых репозиториев; e2e — если изменились sdk-go или два Go-репозитория
PORT=7531 gate4ai/scripts/stand.sh                                     # dev-стенд: daemon из исходников на 127.0.0.1:$PORT, данные в .stand/, коннекторы из папок с --watch
STAND_PUBLIC=1 PORT=7531 gate4ai/scripts/stand.sh                      # то же, плюс адрес в relay.gate4.ai для claude.ai
```

Стенд не трогает установленный `gate4ai`: свои данные, сокеты в `/tmp`, хранилище ключей в файле, без трея и автозапуска.
