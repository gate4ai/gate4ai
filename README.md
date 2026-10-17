# gate4.ai — страж вашего компьютера для AI-агентов

gate4.ai — страж для вашего компьютера: через него любой AI-агент (Claude.ai, ChatGPT, Claude Code, OpenClaw, Hermes) получает ограниченный и настраиваемый доступ к разнообразным [коннекторам](https://gate4.ai/connectors) — например, к почте, файлам, браузеру и сайтам, у которых нет API.

Вы сами настраиваете, на что именно есть права у каждого AI-ассистента: что разрешено сразу, что запрещено, а что требует вашего подтверждения (уведомление, Telegram, Web UI). Ни AI-ассистент, ни коннекторы и скиллы не знают ваших паролей: секреты подставляет сам gate4ai, а каждое действие попадает в журнал с доказательством.

Одни правила работают для всех ваших агентов: локальных (через CLI или MCP по stdio) и облачных (через relay или ваш HTTPS-адрес). Подробное описание продукта — в [спецификации](docs/specification.md).

## Установка

macOS и Linux:

```sh
curl -fsSL https://gate4.ai/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://gate4.ai/install.ps1 | iex
```

Установщик определяет ОС и архитектуру, скачивает последний релиз, сверяет контрольные суммы и кладёт `gate4ai` и `gate4ai-tray` (значок в трее: статус, «Разрешить / Отклонить», открытие Web UI) в каталог из `PATH`. Проверка: `gate4ai --version`. С `GATE4AI_START=1` (`curl -fsSL https://gate4.ai/install.sh | GATE4AI_START=1 sh`) установщик сразу включает автозапуск daemon (шаг 1).

Вручную — архивы со страницы [Releases](https://github.com/gate4ai/gate4ai/releases) (контрольные суммы подписаны [cosign](https://github.com/sigstore/cosign)) или из исходников: `go install github.com/gate4ai/gate4ai/cmd/gate4ai@latest` (Go 1.27+).

## Первый запуск

### 1. Daemon

```sh
gate4ai service install    # daemon сейчас и при каждом входе в систему, трей — в автозапуск
gate4ai ui                 # Web UI в браузере
```

Daemon держит политики, секреты, журнал и коннекторы. Автозапуск: systemd --user (Linux; без systemd — автозапуск рабочего стола), LaunchAgent (macOS), ключ `Run` в реестре пользователя (Windows); убрать — `gate4ai service uninstall`, проверить — `gate4ai service status`.

Без автозапуска: `gate4ai daemon start` — в фоне до выхода из системы (`gate4ai daemon stop` — остановить), `gate4ai daemon` — в этом терминале (остальные команды — в другом). `gate4ai ui`, запущенный в терминале, сам поднимает daemon в фоне, если он не работает; `gate4ai status` — работает ли daemon. Журнал фонового daemon — `daemon.log` в каталоге данных.

Данные лежат в `~/.local/state/gate4ai` (Linux), `~/Library/Application Support/gate4ai` (macOS), `%LOCALAPPDATA%\gate4ai` (Windows); ключи — в системном keychain. На сервере без keychain: `gate4ai service install -- --keystore file` (или `gate4ai daemon --keystore file`); чтобы служба работала без входа в систему — `loginctl enable-linger $USER`.

Дальше всё можно сделать в Web UI: на странице «Обзор» — чек-лист первого запуска. «Коннекторы» — магазин с установкой в один клик и настройками (папки, почтовый ящик, сайты); «Агенты» — мастер добавления с автоподключением Claude Code, Claude Desktop, Cursor, VS Code / Copilot, Codex CLI и Gemini CLI и временем последнего обращения агента; «Правила» — конструктор правил и готовые шаблоны (YAML — для технических пользователей); «Подтверждения» — каналы уведомлений (рабочий стол, Telegram с мастером настройки, браузерный push) со статусом и статистикой; «Настройки» — relay, автозапуск, вход с телефона по QR-коду. Агент может сам изменить настройки (`gate4ai.change_policy`) — как любое изменяющее действие, по умолчанию с вашего подтверждения в любом канале; правилом это можно разрешить или запретить. Ниже — то же из терминала.

### 2. Политика и магазин коннекторов

Создайте `config.yaml`. Пример ниже подключает магазин коннекторов, заводит одного агента и даёт ему читать заметки в `~/Documents/notes` без вопросов, а дописывать — только с вашего подтверждения:

```yaml
store:
  registry: https://raw.githubusercontent.com/gate4ai/registry/main/index.json
  registry_keys: ["ed25519:yDcbQuNCGqSqHSt08HdvW5/fNyWRW8FIXtKiL91hqtw="]
agents:
  my-agent: { profile: assistant }
profiles:
  assistant:
    connectors: [files]
    rules:
      - id: notes-read
        action: files.read
        when: glob(path, "~/Documents/notes/**")
        effect: allow
      - id: notes-append
        action: files.append
        when: glob(path, "~/Documents/notes/**")
        effect: ask
connectors:
  files:
    roots: ["~/Documents/notes"]
```

Правила ссылаются на действия коннекторов, поэтому сначала поставьте коннекторы, затем примените политику целиком. Магазин коннекторов в начальной конфигурации нового daemon уже подключён, а коннекторы `files` и `browser` новый daemon ставит из него сам (подпись проверяется, нужна сеть; без сети — при следующей возможности). Любой установленный коннектор (и эти два, и поставленные вами) daemon добавляет в профиль `default` с правилом «спросить» (`ask`) на каждое его действие, включая чтение, — смотрите `gate4ai policy show`; ваши правила на те же действия не трогаются. Папки (`roots`) и сайты (`allowed_domains`) пусты, пока вы их не зададите. Остальные коннекторы ставятся командой:

```sh
# 1) коннекторы: при установке показываются их возможности и спрашивается согласие
gate4ai connectors list                 # files и browser уже стоят
gate4ai connectors install email

# 2) рабочая политика
gate4ai policy set config.yaml -m "первая политика"
gate4ai doctor
```

Свой коннектор ставится прямо из папки с исходниками: `gate4ai connectors install ./my-connector --watch` — собирает, ставит и пересобирает на каждое сохранение (подробнее — [docs/quickstart.md](docs/quickstart.md)).

Если политика уже была задана без раздела `store`, сначала добавьте в неё `store` из примера выше (`gate4ai policy set`), затем ставьте коннекторы.

Пароли и токены в конфигурацию не пишутся: там только алиасы вида `"@mail.work"`, а значения кладутся в хранилище (из stdin, в историю shell не попадают):

```sh
printf %s 'пароль-приложения' | gate4ai vault set mail.work
```

Полная модель конфигурации — почта, браузер, лимиты, Telegram, песочница — в [docs/quickstart.md](docs/quickstart.md) и разделе «Политики» [спецификации](docs/specification.md#политики).

### 3. Подключение агента

**Агент с shell (Claude Code, Hermes, OpenClaw) — CLI + skill:**

```sh
gate4ai agents token my-agent          # токен показывается один раз
export GATE4AI_TOKEN=g4a_…
gate4ai tools                          # что разрешено профилю
gate4ai files read --path ~/Documents/notes/todo.md
gate4ai skill > SKILL.md               # инструкция для агента
```

Коды выхода: `0` ok, `1` прочие ошибки (в том числе неверный или отозванный токен), `2` запрещено политикой, `3` ждёт подтверждения, `4` проверка не прошла, `5` неверные аргументы или неизвестная команда, `6` daemon недоступен. Агенты и скрипты получают JSON; ошибка — `code`, `message`, а также `hint` (что сделать) и `detail` (подробности), если они есть. Человеку в терминале (stdout — терминал) списки и состояние показываются таблицей, а ошибки — текстом; JSON и там — `--json` или `GATE4AI_OUTPUT=json`.

**MCP по stdio** (Claude Code, Claude Desktop и др.):

```json
{"mcpServers": {"gate4ai": {"command": "gate4ai", "args": ["mcp"], "env": {"GATE4AI_TOKEN": "g4a_…"}}}}
```

**Изолированный локальный агент** — сеть и файлы только через gate4ai:

```sh
gate4ai run claude-code --workspace ~/projects/app
```

**Облачные агенты (Claude.ai, ChatGPT)** — через relay, без открытых портов:

```sh
gate4ai relay register --url https://relay.gate4.ai
gate4ai relay status                   # mcp_url: https://<device-id>.relay.gate4.ai/mcp
gate4ai agents connect my-agent        # одноразовый код подключения
```

Добавьте `mcp_url` в агенте как custom connector и введите код на странице авторизации. Свой HTTPS-адрес вместо relay — `gate4ai daemon --public …`, см. [docs/quickstart.md](docs/quickstart.md#3-агенты).

### 4. Подтверждения и журнал

```sh
gate4ai approvals list                 # запросы, которые ждут вашего решения
gate4ai approvals approve <id>         # или кнопки в трее, Telegram, Web UI
gate4ai ui                             # Web UI в браузере
gate4ai log                            # журнал действий
gate4ai journal verify                 # целостность журнала
```

## Документация

- [Спецификация продукта](docs/specification.md)
- [Подробный запуск: конфигурация, агенты, relay, свой relay](docs/quickstart.md)
- [Устройство кода](docs/architecture.md) · [Коннекторы](docs/connector-architecture.md) · [Секреты и учётки](docs/secret-architecture.md)
- [Разработка: сборка и тесты](docs/quickstart.md#сборка) · [Публикация](docs/publishing.md) · [План работ](PLAN.md)

## Лицензия

[AGPL-3.0-only](LICENSE) с [исключением](LICENSE-EXCEPTION): коннекторы и auth-модули, работающие через ABI, могут иметь любую лицензию. Коннекторы и relay — отдельные репозитории под AGPL-3.0, SDK — Apache-2.0. Подробности — в [NOTICE](NOTICE).
