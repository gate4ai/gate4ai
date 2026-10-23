# gate4.ai — сборка и запуск

Спецификация продукта — [specification.md](specification.md), краткая инструкция — [README.md](../README.md).

## Сборка

Нужен Go 1.27+. Всё на чистом Go (SQLite — `modernc.org/sqlite`), кросс-сборка под macOS, Linux и Windows:

```sh
make bin             # bin/gate4ai, bin/gate4ai-tray, bin/gate4ai-relay
make connectors      # bin/connectors/{mail,browser}.g4c — пакеты официальных коннекторов
./scripts/ci.sh      # SPDX, направление зависимостей, build / vet / test, WASM + кросс-сборка darwin и windows (в CI — матрицей build)
./scripts/ci.sh e2e  # сквозные тесты e2e/
```

Всё — один репозиторий и один Go-модуль: ядро, `sdk-go/`, `relay/`, коннекторы (`connectors/`) и сквозные тесты (`e2e/`).

Положите `gate4ai` в `PATH`.

| Бинарник | Что это |
|---|---|
| `gate4ai` | единственный бинарник ядра: `gate4ai daemon` — политики, подтверждения, секреты, журнал, коннекторы, Web UI, Egress Proxy, туннель к relay; `gate4ai mcp` — MCP-сервер на stdio; остальные команды — CLI для агентов, скриптов и человека |
| `gate4ai-relay` | relay (необязателен — нужен, только если нет своего HTTPS до daemon): публичный адрес устройства, TLS-passthrough, DNS для ACME, свидетель журнала |
| `gate4ai-tray` | трей (необязателен): статус daemon, уведомления о запросах подтверждения, «Разрешить / Отклонить» (для `gate4ai.*` — самонастройки агентом — только «Отклонить» и переход в Web UI: разрешить можно там, где видны все правки), открытие Web UI. Ссылку входа трей и `gate4ai ui` передают браузеру через страницу-файл 0600 в личном кэше (`~/.cache/gate4ai/open/`, на macOS — `~/Library/Caches/…`), а не в командной строке, которую видят другие пользователи компьютера (кроме Windows и snap-браузеров). Ходит в сокет владельца, как CLI. На macOS собирается с cgo (`CGO_ENABLED=1 go build ./cmd/gate4ai-tray` на macOS) |

## 1. Daemon и коннекторы

```sh
gate4ai daemon start             # в фоне; первый запуск: агентов нет, files встроен, магазин подключён, browser ставится из него (автозапуск: gate4ai service install)
gate4ai connectors install bin/connectors/mail.g4c --dev
gate4ai connectors install bin/connectors/browser.g4c --dev
gate4ai connectors list
```

Файлы — встроенный коннектор `files` (часть ядра, из магазина не ставится): в начальной конфигурации он в профиле `default` с корнем `~/Documents` (`connectors.files.roots`; скрытые файлы и папки ниже корня, ключи и данные приложений закрыты всегда — открывают `allow_hidden: true` и `allow_paths`) и правилом `ask` на каждое действие. Первый запуск без `--config` сам ставит из магазина `browser` (проверенный gate4.ai, согласие на его возможности — без вопроса; без сети — повтор в фоне и при следующем запуске). Своя сборка (`--dev`, как выше) заменяет поставленную из магазина; поставленный владельцем коннектор daemon не трогает. С `--config` и на уже настроенной установке сам ничего не ставит.

Каждый установленный или обновлённый коннектор daemon добавляет в профиль `default` (если он есть) и пишет туда правило `ask` (`<коннектор>-<действие>-ask`) на каждое действие, для которого в этом профиле ещё нет правила, — в том числе на чтение, у которого по умолчанию `allow`; действия только для других коннекторов (`service_only`) — без правила. Правила владельца на эти действия остаются как есть.

Коннекторы магазина — WASM-модули: всё, что им можно, объявлено в манифесте (`capabilities`) и показывается при установке; без согласия владельца коннектор не ставится, а обновление с новыми возможностями требует согласия заново. Исключение — нативные пакеты (`runtime: native`: `browser`): отдельный процесс под надзором daemon, без песочницы, с правами пользователя; ставятся только от gate4.ai (или как своя сборка с `--dev`), на экране согласия — предупреждение.

**Вызовы между коннекторами.** Коннектор вызывает действия другого через `service_call` (поставщик перечисляет их в `services`, потребитель — в `capabilities.services` с мажором `contract` и `params`, например сайтами для браузера); каждое действие потребителя перечисляет свои подвызовы (`calls`), и хост пропускает из него только их, не выше его класса. Подвызовы видны в описании инструмента, запросе подтверждения и на экране согласия, поэтому разрешение действия покрывает их — повторно не спрашивается; правило `deny` на действие поставщика действует и во вложенном вызове. Типизированный клиент — `gate4ai contract gen <манифест поставщика> -o client.go`, совместимость нового манифеста — `gate4ai contract diff old.yaml new.yaml`. `--dev` — пакет собственной сборки без подписи доверенного издателя. Из магазина: `gate4ai connectors install email` (нужны `store.registry` и `store.registry_keys` в конфигурации), `gate4ai connectors update <имя>`, `gate4ai connectors refresh` — список отозванных версий. `connectors install` принимает и адрес `https://…/*.g4c` (пакет скачивается и проверяется так же, как файл). В Web UI: **Коннекторы → Поставить не из магазина** — путь к папке с исходниками или файлу `.g4c` на этом компьютере либо адрес сборки; галочка «своя сборка» — то же, что `--dev`.

**Разработка коннектора — прямо из папки с исходниками** (без `pack` и `.g4c`):

```sh
gate4ai connectors install ./connectors/mail           # собрать в WASM и поставить (своя сборка, как --dev)
gate4ai connectors update email                        # пересобрать из той же папки после правок
gate4ai connectors install ./connectors/mail --watch   # пересобирать и переставлять на каждое сохранение
```

Заготовка коннектора к сайту поверх браузера — `gate4ai connectors new <имя> --site <домен>`; как исследовать сайт в сеансе браузера и писать коннектор вместе с агентом — [connector-authoring.md](connector-authoring.md). Лог коннектора — `gate4ai connectors logs <имя>` (вместе с ошибками действий); у своей сборки строки лога за вызов приходят и в ответе (поле `log`). Подвызовы действия — `gate4ai log --parent '#хэш'`. Агент ставит свою сборку без сокета владельца — `gate4ai.change_policy` с `install_dev_connector {folder}` (подтверждает владелец). Личные данные из записанного сеанса браузера перед фикстурой — `gate4ai fixture scrub`.

Сборку делает CLI в вашем окружении (ваш Go, `go.mod` папки или выше, `GOFLAGS`), ошибки компиляции видны сразу; daemon записывает папку источником установки (`dir:<путь>` в `gate4ai connectors list`). `--watch` следит за `.go`, `go.mod`, `go.sum` и `manifest.yaml`; ошибка сборки печатается, наблюдение продолжается. Если изменились возможности в манифесте — согласие спрашивается заново (`--yes` — согласиться без вопроса).

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
    # env: каждый `gate4ai run` получает в ANTHROPIC_API_KEY свою одноразовую заглушку,
    # Egress Proxy ставит вместо неё настоящий ключ (только по HTTPS к api.anthropic.com)
    - { domain: api.anthropic.com, header: X-Api-Key, env: ANTHROPIC_API_KEY, placeholder: sk-ant-gate4ai, secret: "@anthropic.key", agents: [claude-code] }
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
gate4ai vault set mail.work                               # спросит значение (ввод скрыт, Enter — готово)
printf %s 'пароль-приложения' | gate4ai vault set mail.work # или из stdin (скрипты); --from-env VAR — из переменной
printf %s '123456:ABC…'       | gate4ai vault set telegram.bot
gate4ai policy set config.yaml -m "секреты добавлены"   # перечитать каналы
```

Сайты, которые агенты открывают в браузере: `gate4ai sites` — список, `gate4ai sites allow login.example.org` — добавить, `gate4ai sites remove …` — убрать.

Проверка: `gate4ai doctor`.

**Вход на сайты по учётке.** Чтобы агент или коннектор входил на сайт сам, не видя пароля, опишите учётку в `credentials` (поля — константы или секреты Vault, отправка, признак успеха, кому доступна) и положите секрет в хранилище:

```yaml
credentials:
  "@site":
    bind: ["https://login.example.org"]
    form:
      fields:
        - { selector: "#login", value: "me@example.org" }
        - { selector: "#password", secret: "@site.password" }   # только в input[type=password]; иначе type: text
      submit: { selector: "button[type=submit]" }
      success: { url_prefix: "https://lk.example.org/" }
    use: [assistant]            # профили агентов
    connectors: [lk_example]    # коннекторы
```

```sh
printf %s 'пароль' | gate4ai vault set site.password
gate4ai browser open --url https://lk.example.org/      # сайт увёл на вход: login_required {account: "@site"}
gate4ai browser login --session s-… --account @site     # logged_in | submitted | still_on_login
```

Сложный вход (несколько шагов, SMS) — коннектор входа: `via: { connector: site_login, action: login }` вместо `form`. Код из SMS он спрашивает у вас: вопрос — в уведомлении и Web UI («Подтверждения»), ответ — `printf %s 123456 | gate4ai approvals answer <id>`. Учётки видны в Web UI (**Настройки → Пароли и учётки**), простая учётка заводится там же, сложная — в YAML политики. Подробнее — [secret-architecture.md](secret-architecture.md) («Учётки входа на сайты»), [connector-authoring.md](connector-authoring.md).

## 3. Агенты

**С shell (CLI + skill):** агент подключается сам — первая команда отвечает кодом (выход `3`, `pairing_required`), агент показывает его в чате, владелец сверяет код и разрешает подключение в Web UI (Агенты) или Telegram; дальше агент работает своим ключом (каталог данных → `agents/<агент>.json`) без перезапуска. По умолчанию — один агент на тип (`claude-code`); `gate4ai pair --here` — отдельный ключ только для агента из этой папки, со своим профилем. Сокет владельца отвечает узнанному агенту `agent_required`; в журнал пишется папка агента.

```sh
gate4ai tools                              # первый раз — код подключения, потом — что доступно профилю
gate4ai pair --here --wait 5m              # ключ только для этой папки, ждать решения владельца
gate4ai mail send --to landlord@example.com --subject "Аренда" --body "…" --wait 10m
gate4ai skill > SKILL.md                   # навык для Claude Code / Hermes / OpenClaw
```

Скрипты и CI — с токеном: `gate4ai agents token claude-ai` (показывается один раз), `export GATE4AI_TOKEN=g4a_…`.

Коды выхода: `0` ok, `1` прочие ошибки, `2` запрещено политикой (`denied_by_policy`), `3` ждёт подтверждения (`pending_approval`), `4` проверка не прошла (`verification_failed`), `5` неверные аргументы или неизвестное действие, `6` daemon недоступен, `7` вызывающему нельзя: неверный или отозванный ключ / токен (`unauthorized`), команда владельца с ключом агента (`owner_only`), агент на сокете владельца или вызов владельца без `--as` (`agent_required`), подключение отклонено (`pairing_rejected`). Ответ — JSON в едином конверте: `{"status": "ok" | "denied" | "pending" | "verification_failed" | "error", "result": …, "error": {"code", "message", "hint", "detail"}}` — `result` при успехе, `error` в остальных случаях (у `pending` — что делать дальше). Длинный текст аргумента — из файла: `--<имя>_file путь` (`-` — stdin).

Claude Code: подключение в Web UI (Агенты → Claude Code) кладёт ключ агента в каталог ключей gate4ai и правило `"Bash(gate4ai *)"` в `permissions.allow` файла `~/.claude/settings.json` (прежний `env.GATE4AI_TOKEN` убирает); `gate4ai doctor` предупреждает, если агент не подключён своим ключом, правила нет или в `env` остался токен.

**MCP по stdio** (Claude Code и др.): `{"mcpServers": {"gate4ai": {"command": "gate4ai", "args": ["mcp"], "env": {"GATE4AI_AGENT": "claude-code"}}}}` — секрета нет, ключ CLI найдёт по имени агента (до подтверждения вызовы отвечают кодом подключения).

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
gate4ai relay status                      # mcp_url: https://<device-id>.relay.gate4.ai
gate4ai agents connect claude-ai          # одноразовый код подключения
```

Добавьте `mcp_url` как custom connector и введите код на странице авторизации.

**Агент с shell на другом компьютере (CLI + skill):** те же команды, адрес устройства — в `GATE4AI_URL`; подключение — тем же кодом (ключ запоминает адрес, дальше `GATE4AI_URL` не нужен; привязки к папке удалённо нет) или токеном:

```sh
export GATE4AI_URL=https://<device-id>.relay.gate4.ai
gate4ai pair --name laptop-claude --wait 5m        # код → владелец подтверждает в Web UI или Telegram
gate4ai tools
# или: export GATE4AI_TOKEN=g4a_…                  # gate4ai agents token <agent> на устройстве
```

**Без relay** (свой сертификат или reverse proxy на этом компьютере):

```sh
gate4ai daemon --public :8443 --public-url https://gate.example.com --public-cert gate.crt --public-key gate.key
gate4ai daemon --public 127.0.0.1:8080 --public-url https://gate.example.com   # TLS снимает reverse proxy
```

Публичный адрес один на всё: сам адрес (`POST`/`DELETE` на корень) — MCP, `/v1/…` — CLI агентов, `/ui/` — Web UI, `/telegram` — вебхук бота.

## 4. Подтверждения, Web UI, журнал

- Подтверждения: Telegram-бот (кнопки «Разрешить / Отклонить / Показать текст»), уведомления ОС, трей, Web UI, `gate4ai approvals approve <id>`.
- Web UI: `gate4ai ui` — откроет браузер по одноразовой ссылке (http://127.0.0.1:7431); на свежей установке — мастер первого запуска (`/setup`). Интерфейс — английский, русский перевод (переключатель в шапке, cookie `g4a_lang`; по умолчанию — `Accept-Language`). С телефона: `gate4ai ui --remote` выдаёт одноразовую ссылку на `<публичный адрес>/ui/` (действует 2 минуты, открывается один раз); удалённо доступны подтверждения и журнал, политики, агенты и секреты меняются только на этом компьютере. Пока устройство не подключено к relay (`gate4ai relay status`: `"online": false`, причина — в `error`, например не выпущен сертификат), ссылка не выдаётся: она бы не открылась. Выпуск сертификата при неудаче повторяется сам (через 5 минут, затем реже, до часа).
- Защита входа (**Настройки → Вход**): три способа — одноразовая ссылка, пароль (хранится хэшем argon2id в State Store; «Задать» / «Сменить» / «Снять» — в окне), подтверждение входа в Telegram-боте (без подключённого бота «Включить» ведёт в мастер бота, он заканчивается кнопкой включения); сочетаются как угодно, нужно пройти все включённые, хотя бы один включён всегда. По умолчанию — только ссылка. С паролем или Telegram ссылка из `gate4ai ui`, `--print`, `--remote` и трея сама сессию не даёт: открывает попытку входа (5 минут, привязана к браузеру cookie), сессию дают пароль и «Разрешить вход» в Telegram (в сообщении — код со страницы входа; Telegram спрашивается после пароля). Без ссылки вход — на постоянном адресе (кнопка «Войти»): `http://127.0.0.1:7431/` и с телефона `<публичный адрес>/ui/` (закладка работает и через неделю). От перебора: после 5 неверных паролей подряд задержка 1 с, удваивается до 15 минут, у удалённого входа свой счётчик; в Telegram одновременно ждёт ответа один запрос входа. `gate4ai ui` пишет, что ещё нужно. Менять защиту можно только из сессии, открытой с защитой, а при заданном пароле — с вводом текущего; смена закрывает остальные сессии. Восстановление: `gate4ai daemon stop && gate4ai daemon --reset-ui-login` (в Telegram придёт уведомление о сбросе). Флаг разовый: `gate4ai daemon restart` его не повторяет, а `gate4ai service install -- …` не записывает в автозапуск.
- Telegram вебхуком вместо polling: `approvals.telegram.webhook: true` (нужен публичный адрес).
- Журнал: `gate4ai log`, `gate4ai show '#a3f9c21e'`, `gate4ai journal verify`, `gate4ai export -o backup.tar.gz --passphrase-env PASS`, офлайн-проверка `gate4ai verify backup.tar.gz --pubkey <hex>`.
- Политики: `gate4ai policy test --agent claude-ai --action email.send --args '{"to":["x@y.z"],"subject":"s"}'`, `gate4ai policy versions`, `gate4ai policy rollback <n>`.

## 5. Свой relay

```sh
gate4ai-relay --domain relay.example.org --listen :443 --dns :53 --ipv4 203.0.113.7 \
  --cert relay.crt --key relay.key --state /var/lib/gate4ai-relay/state.json
```

Зона `relay.example.org` делегируется (NS) на этот сервер: relay отвечает A-записями для устройств и TXT для ACME DNS-01. Для разработки — `--self-signed` и в конфигурации устройства `relay: {ca_file: relay-ca.pem, self_signed: true}`.

Метрики, логи и трейсы relay (`--metrics`, `--otlp`), стек Grafana + Prometheus + Loki + Tempo на сервере relay и дашборд — [relay/docs/observability.md](../relay/docs/observability.md).

## Тесты

`go test ./...` — модульные тесты всех пакетов, интеграционные тесты daemon по сокетам, OAuth-поток, relay с passthrough, Egress Proxy, WASM-хост на тестовом коннекторе `testdata/probe` (в том числе вход хостом в IMAP на заглушке). Поток из спецификации на собранных бинарниках и настоящих коннекторах — `e2e/` (`./scripts/ci.sh e2e`).

## Тестовый стенд

Отдельный daemon рядом с установленным, тем же `gate4ai`, со своими данными и сокетами: установленный экземпляр (его данные, сокеты, связка ключей, трей) он не трогает.

```sh
mkdir -p /tmp/g4stand
export GATE4AI_HOME=$PWD/.stand GATE4AI_SOCKET=/tmp/g4stand/owner.sock GATE4AI_AGENT_SOCKET=/tmp/g4stand/agent.sock
# стенд, который запускает сам агент (Claude Code): GATE4AI_OWNER_CHECK=off — сокет владельца не отличает агента от человека
gate4ai daemon --ui 127.0.0.1:7531 --keystore file &       # ключи в файле, а не в связке ОС
gate4ai connectors install connectors/mail --yes --watch   # коннектор из каталога исходников, пересобирается при правках
gate4ai ui --print                                         # одноразовая ссылка на Web UI (2 минуты)
gate4ai relay register --url https://relay.gate4.ai        # адрес для claude.ai: gate4ai relay status
```

Сокеты — в коротком пути: у unix-сокета ограничена длина. Прод-relay `relay.gate4.ai` можно использовать и для тестовых стендов. Каждая регистрация — новое устройство и новый сертификат на `*.relay.gate4.ai` (у Let's Encrypt лимит 50 в неделю), поэтому храните `GATE4AI_HOME` стенда между запусками, а не регистрируйтесь заново.
