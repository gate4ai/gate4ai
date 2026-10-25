# gate4.ai — сборка и запуск

Спецификация продукта — [specification.md](specification.md), краткая инструкция — [README.md](../README.md).

## Сборка

Нужен Go 1.27+. Всё на чистом Go (SQLite — `modernc.org/sqlite`), кросс-сборка под macOS, Linux и Windows:

```sh
make bin             # bin/gate4ai, bin/gate4ai-tray, bin/gate4ai-relay
./scripts/ci.sh      # SPDX, направление зависимостей, build / vet / test, WASM-сборка sdk-go + кросс-сборка darwin и windows (в CI — матрицей build)
./scripts/ci.sh e2e  # сквозные тесты e2e/
```

Всё — один репозиторий и один Go-модуль: ядро (в нём встроенные Браузер и Файлы — `internal/connectors/builtin/`), `sdk-go/`, `relay/` и сквозные тесты (`e2e/`). Пакетов коннекторов сборка не делает: коннекторы магазина — отдельные репозитории издателей.

Положите `gate4ai` в `PATH`.

| Бинарник | Что это |
|---|---|
| `gate4ai` | единственный бинарник ядра: `gate4ai daemon` — политики, списки доступа, подтверждения, секреты, журнал, встроенные Браузер и Файлы, коннекторы магазина, Web UI, Egress Proxy, туннель к relay; `gate4ai mcp` — MCP-сервер на stdio; остальные команды — CLI для агентов, скриптов и человека |
| `gate4ai-relay` | relay (необязателен — нужен, только если нет своего HTTPS до daemon): публичный адрес устройства, TLS-passthrough, DNS для ACME, свидетель журнала |
| `gate4ai-tray` | трей (необязателен): статус daemon, уведомления о запросах подтверждения, «Разрешить / Отклонить» (для `gate4ai.*` — самонастройки агентом — только «Отклонить» и переход в Web UI: разрешить можно там, где видны все правки), открытие Web UI. Ссылку входа трей и `gate4ai ui` передают браузеру через страницу-файл 0600 в личном кэше (`~/.cache/gate4ai/open/`, на macOS — `~/Library/Caches/…`), а не в командной строке, которую видят другие пользователи компьютера (кроме Windows и snap-браузеров). Ходит в сокет владельца, как CLI. На macOS собирается с cgo (`CGO_ENABLED=1 go build ./cmd/gate4ai-tray` на macOS) |

## 1. Daemon, Браузер и Файлы

```sh
gate4ai daemon start             # в фоне; первый запуск: агентов нет, Браузер и Файлы встроены, магазин подключён (автозапуск: gate4ai service install)
gate4ai access                   # списки доступа: сайты Браузера и папки Файлов
gate4ai connectors list          # коннекторы магазина (на свежей установке пусто)
```

Браузер и Файлы — часть ядра (`runtime: builtin`, из магазина не ставятся). Начальная конфигурация без `--config`: профиль `default` с `files` и `browser`, списки доступа — любой сайт (`*`) и `~/Documents` в режиме `ask` (каждое действие спрашивает вас), правил нет; скрытые файлы и папки, ключи и данные приложений Файлы не открывают никогда (открывают `allow_hidden: true` и `allow_paths`). Из магазина daemon сам ничего не ставит. Старая установка с пакетами `browser` / `email` из магазина при старте новой версии удаляет их, убирает почту из политики и переносит прежние `connectors.browser.allowed_domains` / `blocked` / `mode` и `connectors.files.roots` в списки доступа (`read` / `deny`) — новой версией политики, с откатом.

**Списки доступа** — первый уровень решения для Браузера и Файлов, общий для всех агентов (`access.sites`, `access.folders` в конфигурации):

```sh
gate4ai access sites set app.example.org ask       # каждое действие на сайте — с подтверждением (режима «только чтение» у сайтов нет)
gate4ai access sites set "*.example.com" allow     # поддомены и сам example.com — всё без вопроса
gate4ai access sites set "*" deny                  # остальные сайты закрыты
gate4ai access folders set ~/Documents/notes append   # чтение, список и дописывание без вопроса, перезапись — с подтверждением
gate4ai access folders remove ~/Documents
gate4ai sites allow account.example.org                 # то же, что access sites set account.example.org allow
```

Режимы сайтов: `allow`, `read`, `connector` (только через коннектор магазина, агенту напрямую — отказ), `ask`, `deny`; у папок ещё `append`. Побеждает самая конкретная строка, при равенстве — строже. Нет строки или `deny` — отказ; правило политики на действие (`allow` / `ask` / `deny`) побеждает строку, без правила действует режим строки. В Web UI то же — **Доступ → Браузер** и **Файлы**; в запросе подтверждения по сайту или папке есть кнопки «Добавить в разрешённые / запрещённые». Агент просит сайт через `gate4ai.change_policy` (`set_site_access`) — с вашим подтверждением.

Каждый установленный или обновлённый коннектор магазина daemon добавляет в профиль `default` (если он есть) и пишет туда правило `ask` (`<коннектор>-<действие>-ask`) на каждое действие, для которого в этом профиле ещё нет правила, — в том числе на чтение, у которого по умолчанию `allow`; действия только для других коннекторов (`service_only`) — без правила. Правила владельца на эти действия остаются как есть. Сайты и папки коннектора (`sites` / `folders` манифеста) при установке копируются в `connectors.<имя>.sites` / `folders` — их вы правите на странице коннектора.

Коннекторы магазина — WASM-модули без сети: сайт им доступен только страницей встроенного браузера (`service_call`), файлы — папками из манифеста в пределах списка Файлов. Всё, что им можно, объявлено в манифесте (`capabilities`, `sites`, `folders`, `credentials`) и показывается при установке; без согласия владельца коннектор не ставится, а обновление с новыми возможностями требует согласия заново.

**Вызовы между коннекторами.** Коннектор вызывает действия другого через `service_call` (поставщик перечисляет их в `services`, потребитель — в `capabilities.services` с мажором `contract`); каждое действие потребителя перечисляет свои подвызовы (`calls`), и хост пропускает из него только их, не выше его класса. Подвызовы видны в описании инструмента, запросе подтверждения и на экране согласия, поэтому разрешение действия покрывает их — повторно не спрашивается; правило `deny` на действие поставщика действует и во вложенном вызове; вызов браузера проходит, только если сайт разрешают и строка коннектора, и список Браузера (строка коннектора `ask` ждёт вашего решения). Типизированный клиент — `gate4ai contract gen <манифест поставщика> -o client.go` (манифест браузера — `internal/connectors/builtin/browser/manifest.yaml`), совместимость нового манифеста — `gate4ai contract diff old.yaml new.yaml`. `--dev` — пакет собственной сборки без подписи доверенного издателя. Из магазина: `gate4ai connectors install <имя>` (нужны `store.registry` и `store.registry_keys` в конфигурации), `gate4ai connectors update <имя>`, `gate4ai connectors refresh` — список отозванных версий. `connectors install` принимает и адрес `https://…/*.g4c` (пакет скачивается и проверяется так же, как файл). Голое имя (`account_example`, `account_example@1.2.0`) — всегда коннектор из магазина, даже если в текущем каталоге есть одноимённый файл или папка (CLI лишь подскажет путь); файл или папку указывайте путём — с `/` (`./account_example`) или с расширением `.g4c`. В Web UI: **Доступ → Коннекторы → Поставить не из магазина** — путь к папке с исходниками или файлу `.g4c` на этом компьютере либо адрес сборки; галочка «своя сборка» — то же, что `--dev`.

**Разработка коннектора — прямо из папки с исходниками** (без `pack` и `.g4c`):

```sh
gate4ai connectors install ./account_example           # собрать в WASM и поставить (своя сборка, как --dev)
gate4ai connectors update account_example              # пересобрать из той же папки после правок
gate4ai connectors install ./account_example --watch   # пересобирать и переставлять на каждое сохранение
```

Заготовка коннектора к сайту поверх браузера — `gate4ai connectors new <имя> --site <домен>`; как исследовать сайт в сеансе браузера и писать коннектор вместе с агентом — [connector-authoring.md](connector-authoring.md); пример — `e2e/testconnector` к тестовому сайту `e2e/testsite` (`go run ./e2e/testsite/cmd/testsite`). Лог коннектора — `gate4ai connectors logs <имя>` (вместе с ошибками действий); у своей сборки строки лога за вызов приходят и в ответе (поле `log`). Подвызовы действия — `gate4ai log --parent '#хэш'`. Агент ставит свою сборку без сокета владельца — `gate4ai.change_policy` с `install_dev_connector {folder}` (подтверждает владелец). Личные данные из записанного сеанса браузера перед фикстурой — `gate4ai fixture scrub`.

Сборку делает CLI в вашем окружении (ваш Go, `go.mod` папки или выше, `GOFLAGS`), ошибки компиляции видны сразу; daemon записывает папку источником установки (`dir:<путь>` в `gate4ai connectors list`). `--watch` следит за `.go`, `go.mod`, `go.sum` и `manifest.yaml`; ошибка сборки печатается, наблюдение продолжается. Если изменились возможности в манифесте — согласие спрашивается заново (`--yes` — согласиться без вопроса).

## 2. Конфигурация

Правила ссылаются на действия установленных коннекторов, поэтому политику задают после установки (Браузер и Файлы установлены всегда). Минимальный `config.yaml` (полная модель — раздел «Политики» [спецификации](specification.md#политики)):

```yaml
access:                         # списки доступа Браузера и Файлов — для всех агентов
  sites:
    - { domain: "*.example.com", access: ask }     # каждое действие — с подтверждением
    - { domain: app.example.org, access: allow }
    - { domain: "*", access: deny }                # остальные сайты закрыты
  folders:
    - { path: "~/Documents/obsidian", access: append }   # чтение, список, дописывание; перезапись — с подтверждением
agents:
  claude-ai:   { profile: assistant }
  claude-code: { profile: dev }
profiles:
  assistant:
    connectors: [files, browser]
    rules:                      # правила уточняют списки доступа: явное правило побеждает строку
      - id: obsidian-journal-protect
        action: files.write
        when: glob(path, "~/Documents/obsidian/journal.md")
        effect: deny
        reason: "Дневник не перезаписывать"
    limits:
      - { id: clicks-per-day, action: browser.act, per_day: 200 }
  dev:
    connectors: [files]
    sandbox:
      allow_domains: [api.anthropic.com, registry.npmjs.org, github.com]
      ask_domains: ["*.githubusercontent.com"]
connectors:
  browser:                    # встроенный: предел текста, скрытые параметры; сайты — в access.sites
    max_text_chars: 30000
    redact_params: [patient_id]
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

Строки `@имя` — алиасы секретов: сами секреты в конфигурации не хранятся, и коннекторы их не видят. Значение алиаса получает только ядро — и встроенный браузер там, где манифест объявляет `capabilities.reveal` (ключ модели). У коннекторов магазина собственных секретов нет: входят они учётками (`credentials`), которые вводит в форму сайта встроенный браузер. Пароль вместо алиаса в настройках коннектора не принимается.

```sh
gate4ai policy set config.yaml -m "первая политика"
```

`gate4ai daemon --config config.yaml` при первом запуске сразу берёт эту конфигурацию — если коннекторы магазина, на которые она ссылается, уже установлены.

Данные — в `~/.local/state/gate4ai` (Linux), `~/Library/Application Support/gate4ai` (macOS), `%LOCALAPPDATA%\gate4ai` (Windows); `GATE4AI_HOME` переопределяет. Ключи — в системном keychain; если его нет (headless Linux), `--keystore file`.

Секреты (значение — из stdin, в историю shell не попадает):

```sh
gate4ai vault set llm.key                                  # спросит значение (ввод скрыт, Enter — готово)
printf %s 'sk-…'        | gate4ai vault set llm.key        # или из stdin (скрипты); --from-env VAR — из переменной
printf %s '123456:ABC…' | gate4ai vault set telegram.bot
gate4ai policy set config.yaml -m "секреты добавлены"   # перечитать каналы
```

Сайты и папки без правки YAML: `gate4ai access` — списки, `gate4ai access sites set <сайт> <режим>` / `remove`, `gate4ai access folders set <папка> <режим>` / `remove`, `gate4ai sites allow <сайт>` — чтение без вопроса.

**Браузер: экран, сеансы, просмотр.** Chrome запускается с выделенным профилем и без порта отладки (CDP по pipe, клиентам — 127.0.0.1 с секретным токеном в адресе; на Windows пока порт). Экран — `connectors.browser.display`: `auto` (по умолчанию: свой экран; Linux без графики — виртуальный Xvfb, который пакет поднимает сам; без Xvfb — `headless`, с предупреждением), `virtual`, `headless`, `host`. Каждый сеанс агента — своё окно; `browser.open` отвечает `view_url`. Владелец: `gate4ai browser sessions` (`--json`), `gate4ai browser sessions close s-…`, `gate4ai browser watch s-…` (`--remote` — ссылка для другого устройства), Web UI → «Браузер» (просмотр вживую, «Взять управление», ответ на `browser.handoff`), трей — пункт «Браузер». Пока сеанс держит владелец, действия агента в нём отвечают `session_held_by_user`. Chrome нет или экран не поднять — `browser_unavailable`; проверка и установка недостающего — `gate4ai browser setup` (`--dry-run` — только показать команды, `--yes` — поставить без вопроса), `gate4ai doctor` показывает то же. Chrome в Docker: песочнице Chrome нужны пространства имён — `--security-opt seccomp=unconfined` (или профиль seccomp Chrome).

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
      success: { url_prefix: "https://account.example.org/" }
    use: [assistant]            # профили агентов
    connectors: [account_example]    # коннекторы
```

```sh
printf %s 'пароль' | gate4ai vault set site.password
gate4ai browser open --url https://account.example.org/      # сайт увёл на вход: login_required {account: "@site"}
gate4ai browser login --session s-… --account @site     # logged_in | submitted | still_on_login
```

Сложный вход (несколько шагов, SMS) — коннектор входа: `via: { connector: site_login, action: login }` вместо `form`. Код из SMS он спрашивает у вас: вопрос — в уведомлении и Web UI («Подтверждения»), ответ — `printf %s 123456 | gate4ai approvals answer <id>`. Учётки видны в Web UI (**Настройки → Пароли и учётки**), простая учётка заводится там же, сложная — в YAML политики. Подробнее — [secret-architecture.md](secret-architecture.md) («Учётки входа на сайты»), [connector-authoring.md](connector-authoring.md).

## 3. Агенты

**С shell (CLI + skill):** агент подключается сам — первая команда отвечает кодом (выход `3`, `pairing_required`), агент показывает его в чате, владелец сверяет код и разрешает подключение в Web UI (Агенты) или Telegram; дальше агент работает своим ключом (каталог данных → `agents/<агент>.json`) без перезапуска. По умолчанию — один агент на тип (`claude-code`); `gate4ai pair --here` — отдельный ключ только для агента из этой папки, со своим профилем. Сокет владельца отвечает узнанному агенту `agent_required`; в журнал пишется папка агента.

```sh
gate4ai tools                              # первый раз — код подключения, потом — что доступно профилю
gate4ai pair --here --wait 5m              # ключ только для этой папки, ждать решения владельца
gate4ai files append --path ~/Documents/obsidian/log.md --text "- done" --wait 10m
gate4ai browser read --url https://app.example.org/
gate4ai skill > SKILL.md                   # навык для Claude Code / Hermes / OpenClaw
gate4ai browser page_screenshot --session s-… --selector main --out main.png   # картинка — в файл, путь — result.file
```

Скрипты и CI — с токеном: `gate4ai agents token claude-ai` (показывается один раз), `export GATE4AI_TOKEN=g4a_…`.

Коды выхода: `0` ok, `1` прочие ошибки, `2` запрещено политикой (`denied_by_policy`), `3` ждёт подтверждения (`pending_approval`), `4` проверка не прошла (`verification_failed`), `5` неверные аргументы или неизвестное действие, `6` daemon недоступен, `7` вызывающему нельзя: неверный или отозванный ключ / токен (`unauthorized`), команда владельца с ключом агента (`owner_only`), агент на сокете владельца или вызов владельца без `--as` (`agent_required`), подключение отклонено (`pairing_rejected`). Ответ — JSON в едином конверте: `{"status": "ok" | "denied" | "pending" | "verification_failed" | "error", "result": …, "error": {"code", "message", "hint", "detail"}}` — `result` при успехе, `error` в остальных случаях (у `pending` — что делать дальше). Длинный текст аргумента — из файла: `--<имя>_file путь` (`-` — stdin).

Картинку и файл из ответа CLI не печатает base64: `--out файл.png` сохраняет их в файл, путь — в `result.file`; если действие ждёт подтверждения — `gate4ai status <id> --wait 10m --out файл` (`page_screenshot` и без `--out` пишет PNG в каталог данных gate4ai, `browser-shots`).

Claude Code: подключение в Web UI (Агенты → Claude Code) кладёт навык `~/.claude/skills/gate4ai/SKILL.md` — короткую обёртку «перед работой выполни `gate4ai skill`»: актуальный текст (действия профиля, правила браузера) агент берёт у установленного бинарника, и навык не устаревает при обновлении; навык прежних версий (полный текст) daemon при старте и `gate4ai skill` заменяют обёрткой. Подключение кладёт и ключ агента в каталог ключей gate4ai и правило `"Bash(gate4ai *)"` в `permissions.allow` файла `~/.claude/settings.json` (прежний `env.GATE4AI_TOKEN` убирает); `gate4ai doctor` предупреждает, если агент не подключён своим ключом, правила нет или в `env` остался токен.

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

**Сервер без графики рядом с OpenClaw** (агент работает в браузере gate4ai, человека зовёт ссылкой на экран браузера): схема установки и доработки — [headless-server.md](headless-server.md).

**Без relay** (свой сертификат или reverse proxy на этом компьютере):

```sh
gate4ai daemon --public :8443 --public-url https://gate.example.com --public-cert gate.crt --public-key gate.key
gate4ai daemon --public 127.0.0.1:8080 --public-url https://gate.example.com   # TLS снимает reverse proxy
```

Публичный адрес один на всё: сам адрес (`POST`/`DELETE` на корень) — MCP, `/v1/…` — CLI агентов, `/ui/` — Web UI, `/telegram` — вебхук бота.

## 4. Подтверждения, Web UI, журнал

- Подтверждения: Telegram-бот (кнопки «Разрешить / Отклонить / Показать текст», у запроса по сайту или папке — «В разрешённые / В запрещённые»), уведомления ОС, трей, Web UI, `gate4ai approvals approve <id>`.
- Web UI: `gate4ai ui` — откроет браузер по адресу http://127.0.0.1:7431/; на свежей установке — сначала форма первого пароля, затем мастер первого запуска (`/setup`): выбранные папки и сайты он записывает в списки доступа. Интерфейс — английский, русский перевод (переключатель в шапке, cookie `g4a_lang`; по умолчанию — `Accept-Language`). С телефона: `gate4ai ui --remote` печатает адрес `<публичный адрес>/ui/` (тот же — QR-кодом в Настройках → Доступ из интернета); удалённо доступны подтверждения и журнал, политики, агенты и секреты меняются только на этом компьютере. Пока устройство не подключено к relay (`gate4ai relay status`: `"online": false`, причина — в `error`, например не выпущен сертификат), адрес не выдаётся: он бы не открылся. Выпуск сертификата при неудаче повторяется сам (через 5 минут, затем реже, до часа).
- Защита входа (**Настройки → Вход**): пароль обязателен (хранится хэшем argon2id в State Store; «Сменить» — в окне, снять нельзя), по желанию — подтверждение входа в Telegram-боте (без подключённого бота «Включить» ведёт в мастер бота, он заканчивается кнопкой включения). Первый пароль задаётся формой на `http://127.0.0.1:7431/`, пока пароля нет (с `/ui/` — нельзя, там только подсказка); задаёт тот, кто открыл страницу первым. Вход — на постоянном адресе (кнопка «Войти» с паролем начинает попытку входа: 5 минут, привязана к браузеру cookie): `http://127.0.0.1:7431/` и с телефона `<публичный адрес>/ui/` (закладка работает и через неделю); с Telegram после пароля — «Разрешить вход» в боте (в сообщении — код со страницы входа). Сессия — 12 часов. От перебора: после 5 неверных паролей подряд задержка 1 с, удваивается до 15 минут, у удалённого входа свой счётчик; в Telegram одновременно ждёт ответа один запрос входа. `gate4ai ui` пишет, что нужно для входа, и предупреждает, пока пароля нет. Менять защиту можно только из сессии и с вводом текущего пароля; смена закрывает остальные сессии. Восстановление: `gate4ai daemon stop && gate4ai daemon --reset-ui-login` (в Telegram придёт уведомление о сбросе) — Web UI снова спросит первый пароль. Флаг разовый: `gate4ai daemon restart` его не повторяет, а `gate4ai service install -- …` не записывает в автозапуск.
- Telegram вебхуком вместо polling: `approvals.telegram.webhook: true` (нужен публичный адрес).
- Журнал: `gate4ai log`, `gate4ai show '#a3f9c21e'`, `gate4ai journal verify`, `gate4ai export -o backup.tar.gz --passphrase-env PASS`, офлайн-проверка `gate4ai verify backup.tar.gz --pubkey <hex>`.
- Политики: `gate4ai policy test --agent claude-ai --action browser.act --args '{"session":"s-1","selector":"button","method":"click"}'`, `gate4ai policy versions`, `gate4ai policy rollback <n>`.

## 5. Свой relay

```sh
gate4ai-relay --domain relay.example.org --listen :443 --dns :53 --ipv4 203.0.113.7 \
  --cert relay.crt --key relay.key --state /var/lib/gate4ai-relay/state.json
```

Зона `relay.example.org` делегируется (NS) на этот сервер: relay отвечает A-записями для устройств и TXT для ACME DNS-01. Для разработки — `--self-signed` и в конфигурации устройства `relay: {ca_file: relay-ca.pem, self_signed: true}`.

Метрики, логи и трейсы relay (`--metrics`, `--otlp`), стек Grafana + Prometheus + Loki + Tempo на сервере relay и дашборд — [relay/docs/observability.md](../relay/docs/observability.md).

## Тесты

`go test ./...` — модульные тесты всех пакетов, интеграционные тесты daemon по сокетам, OAuth-поток, relay с passthrough, Egress Proxy, WASM-хост на тестовом коннекторе `testdata/probe`, браузер на фейковом CDP. Поток из спецификации на собранных бинарниках, встроенных Браузере и Файлах и тестовом сайте `e2e/testsite` с WASM-коннектором `e2e/testconnector` — `e2e/` (`./scripts/ci.sh e2e`). Daemon в e2e и на стенде (`dev/scripts/stand.sh`) запускается с `GATE4AI_DESKTOP_NOTIFY=log`: уведомления daemon на рабочем столе (запросы подтверждения вроде «codex хочет выполнить browser.post», запросы агента на подключение, коды, отказы) не всплывают, а пишутся в журнал daemon (`desktop notification` с заголовком и текстом); решения — как обычно, в Web UI, Telegram или CLI.

## Тестовый стенд

Отдельный daemon рядом с установленным, тем же `gate4ai`, со своими данными и сокетами: установленный экземпляр (его данные, сокеты, связка ключей, трей) он не трогает.

```sh
mkdir -p /tmp/g4stand
export GATE4AI_HOME=$PWD/.stand GATE4AI_SOCKET=/tmp/g4stand/owner.sock GATE4AI_AGENT_SOCKET=/tmp/g4stand/agent.sock
export GATE4AI_DESKTOP_NOTIFY=log   # уведомления daemon — в его журнал, без всплывающих окон
# стенд, который запускает сам агент (Claude Code): сборка go build -tags gate4ai_devstand ./cmd/gate4ai
# и GATE4AI_OWNER_CHECK=off — сокет владельца не отличает агента от человека (в выпуске переменная не действует)
gate4ai daemon --ui 127.0.0.1:7531 --keystore file &       # ключи в файле, а не в связке ОС
go run ./e2e/testsite/cmd/testsite &                       # тестовый сайт на 127.0.0.1:8099 (учётка — на /login)
gate4ai connectors install e2e/testconnector --yes --watch  # коннектор к нему из каталога исходников, пересобирается при правках
gate4ai ui --print                                         # адрес Web UI (вход по паролю)
gate4ai relay register --url https://relay.gate4.ai        # адрес для claude.ai: gate4ai relay status
```

Сокеты — в коротком пути: у unix-сокета ограничена длина. Прод-relay `relay.gate4.ai` можно использовать и для тестовых стендов. Каждая регистрация — новое устройство и новый сертификат на `*.relay.gate4.ai` (у Let's Encrypt лимит 50 в неделю), поэтому храните `GATE4AI_HOME` стенда между запусками, а не регистрируйтесь заново.
