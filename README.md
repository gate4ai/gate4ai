# gate4.ai — локальный шлюз с политиками для AI-агентов

gate4.ai — приложение для вашего компьютера, через которое любой AI-агент (Claude.ai, ChatGPT, Claude Code, OpenClaw, Hermes) получает доступ к почте, файлам, браузеру и сервисам этого компьютера. Агент не видит пароли и не действует напрямую: каждый его запрос проходит через политики, которые разрешают, запрещают или спрашивают вас (уведомление, Telegram, Web UI). Секреты подставляет сам gate4ai, а каждое действие попадает в журнал с доказательством.

Одни правила работают для всех ваших агентов: локальных (через CLI или MCP по stdio) и облачных (через relay или ваш HTTPS-адрес). Подробное описание продукта — в [спецификации](docs/specification.md).

> Статус: прототип (версия 0.x). Интерфейсы и формат конфигурации могут меняться.

## Установка

### Готовые сборки

Скачайте архив для своей ОС со страницы [Releases](https://github.com/gate4ai/gate4ai/releases) (`gate4ai_<версия>_<ОС>_<архитектура>.tar.gz`, для Windows — `.zip`), распакуйте и положите `gate4ai` в `PATH`:

```sh
tar xzf gate4ai_*_linux_amd64.tar.gz
install -m 755 gate4ai ~/.local/bin/
gate4ai --version
```

Необязательный значок в трее (статус, «Разрешить / Отклонить», открытие Web UI) — в отдельных архивах `gate4ai-tray_<версия>_<ОС>_<архитектура>`; его запускают рядом с работающим daemon.

Проверка подлинности (необязательно): `checksums.txt` подписан [cosign](https://github.com/sigstore/cosign) без ключа:

```sh
sha256sum --ignore-missing -c checksums.txt
cosign verify-blob checksums.txt --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github.com/gate4ai/gate4ai/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Из исходников

Нужен Go 1.27+:

```sh
go install github.com/gate4ai/gate4ai/cmd/gate4ai@latest
go install github.com/gate4ai/gate4ai/cmd/gate4ai-tray@latest   # необязательно; на macOS нужен cgo (Xcode Command Line Tools)
```

Бинарник окажется в `$(go env GOPATH)/bin` — добавьте этот каталог в `PATH`.

## Первый запуск

### 1. Daemon

```sh
gate4ai daemon
```

Daemon держит политики, секреты, журнал и коннекторы. Данные лежат в `~/.local/state/gate4ai` (Linux), `~/Library/Application Support/gate4ai` (macOS), `%LOCALAPPDATA%\gate4ai` (Windows); ключи — в системном keychain. На сервере без keychain: `gate4ai daemon --keystore file`.

Остальные команды выполняйте в другом терминале.

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

Правила ссылаются на действия коннекторов, поэтому сначала включите магазин, затем поставьте коннекторы, затем примените политику целиком:

```sh
# 1) только магазин: временная политика без правил
cat > store.yaml <<'YAML'
store:
  registry: https://raw.githubusercontent.com/gate4ai/registry/main/index.json
  registry_keys: ["ed25519:yDcbQuNCGqSqHSt08HdvW5/fNyWRW8FIXtKiL91hqtw="]
profiles:
  assistant: {}
YAML
gate4ai policy set store.yaml -m "магазин"

# 2) коннекторы: при установке показываются их возможности и спрашивается согласие
gate4ai connectors install files
gate4ai connectors install email
gate4ai connectors install browser
gate4ai connectors list

# 3) рабочая политика
gate4ai policy set config.yaml -m "первая политика"
gate4ai doctor
```

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

Коды выхода: `0` ok, `2` запрещено политикой, `3` ждёт подтверждения, `4` проверка не прошла, `5` неверные аргументы, `6` daemon недоступен.

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

[FSL-1.1-ALv2](LICENSE): исходный код открыт, каждая версия через два года переходит под Apache-2.0. Коннекторы — отдельные репозитории под AGPL-3.0 с исключением для хоста, SDK — Apache-2.0. Подробности — в [NOTICE](NOTICE).
