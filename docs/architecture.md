# Устройство кода

Один репозиторий `gate4ai/gate4ai` и один Go-модуль `github.com/gate4ai/gate4ai`: ядро (`cmd/`, `internal/`), `sdk-go/` (Apache-2.0), `relay/`, `connectors/`, `e2e/`. Направление зависимостей проверяет CI (`scripts/deps-check.sh`): `sdk-go` — только сам себя; `relay` — сам себя и `sdk-go`; ядро — себя и `sdk-go`, `relay` — только в тестах; коннектор — свой каталог, `sdk-go` и ядро, другой коннектор — никогда; `e2e` не импортирует никто.

| Компонент [спецификации](specification.md) | Пакет |
|---|---|
| Модель конфигурации, Config Store (версии, откат, права владельца) | `internal/config` |
| Policy Engine (CEL, deny > ask > allow, умолчания по классу, лимиты) | `internal/policy` |
| Action Pipeline (8 шагов) | `internal/pipeline` |
| State Store (счётчики, очередь подтверждений, метки происхождения, токены) | `internal/state` |
| Журнал: квитанции RFC 8785, цепочка, AEAD, доказательства age, чекпоинты, экспорт | `internal/audit` |
| Vault (keychain, алиасы, маскирование) | `internal/secrets/vault`, `internal/secrets/keys` |
| Учётки входа на сайты (`credentials`): доступ, форма и значения только для `browser.login` / `browser.fill_secret`, вход коннектором входа (`via`), события `secret.use`; вопросы человеку с вводом кода | `internal/config/credentials.go`, `internal/pipeline/credentials.go`, `internal/daemon/questions.go` |
| Approval Service: desktop, Telegram | `internal/approval` |
| Манифест, ABI v2, функции хоста, проверка возможностей, PDK и тестовый хост коннекторов | `sdk-go/` (`github.com/gate4ai/gate4ai/sdk-go`, Apache-2.0) |
| Реестр коннекторов, WASM-хост (wazero), установленные коннекторы | `internal/connectors`, `internal/connectors/wasm`, `internal/connectors/store` |
| Нативные коннекторы (`runtime: native`): процесс под надзором daemon, JSON-RPC по stdio, функции хоста с проверкой возможностей, `reveal`, перезапуск; сборка под платформу | `internal/connectors/native`, протокол — `sdk-go/native` |
| Вызовы между коннекторами (`service_call`): маршрутизация через Action Pipeline (политика агента корневого действия, квитанция с `parent`, глубина, циклы), `end_call` | `internal/pipeline/nested.go`, `internal/daemon/services.go`; контракты — `sdk-go/contract`, CLI `gate4ai contract gen|diff` — `internal/front/cli/contract.go` |
| Встроенные коннекторы на PDK (`runtime: builtin`, тот же код и те же функции хоста, что у WASM): файлы | `internal/connectors/builtin/files`, адаптер — `internal/connectors/wasm/builtin.go` (`Engine.Builtin`) |
| Коннекторы (AGPL-3.0): почта (WASM), браузер — нативный пакет (воркер: Chrome, CDP, Stagehand, Executor — `worker/`) | `connectors/mail`, `connectors/browser` |
| daemon, API на сокетах владельца и агентов | `internal/daemon`, `internal/platform/ipc` |
| Лог по коннектору (кольцевой буфер записей slog с атрибутом `connector`), `gate4ai connectors logs`, поле `log` в ответе своей сборки | `internal/daemon/connlog.go`, `Pipeline.ConnectorLog` |
| Заготовка коннектора к сайту (`gate4ai connectors new`) | `internal/front/cli/scaffold.go`; как писать — [connector-authoring.md](connector-authoring.md) |
| Обезличивание фикстур (`gate4ai fixture scrub`) | `internal/front/cli/fixture.go` |
| Маскирование одноразовых кодов и токенов в адресах (результаты, журнал) | `internal/pipeline/urlmask.go` |
| Отмена вызова нативного пакета (`cancel`, срок вызова в процессе) | `sdk-go/native/serve.go`, `internal/connectors/native/native.go` |
| Публичный адрес устройства: один обработчик для relay и прямого HTTPS (`--public`), разделение по путям — MCP (`POST`/`DELETE` на корень; `/mcp` — совместимость) и OAuth (`resource` — адрес устройства), `/v1/{whoami,tools,call,status}` (удалённый CLI), `/ui/` (удалённый Web UI), `/telegram` (вебхук); у каждого пути своя аутентификация, API владельца нет | `internal/daemon` (`PublicHandler`, `AddPublic`, `ServePublic`), `cmd/gate4ai/setup.go`, тест изоляции `cmd/gate4ai/public_test.go` |
| CLI, skill | `internal/front/cli`, `internal/front/skill`; ошибки для человека и агента (`hint`, `detail`) — `errors.go`; `gate4ai daemon start/stop/status` — `daemonctl.go`; автозапуск `gate4ai service` — `service.go` поверх `internal/platform/autostart`; UX путей пользователя — [cli-ux.md](cli-ux.md) |
| MCP Front (stdio, Streamable HTTP), OAuth 2.1 | `internal/front/mcp`, `internal/front/mcpcli`, `internal/front/oauth` |
| Relay — сервер (туннель, passthrough, DNS/ACME, свидетель) | `relay/`, `relay/cmd/gate4ai-relay` |
| Relay — клиент устройства | `internal/relayclient` |
| Web UI: обзор с чек-листом, агенты (последнее обращение, автоподключение), магазин коннекторов, конструктор правил и шаблоны, каналы подтверждений, настройки — [ui-plan.md](ui-plan.md) | `internal/front/webui` (`pages.go`, `rules.go` — блоки условий → CEL и шаблоны, `connsettings.go` — поля настроек из `${setting:…}`) |
| Защита входа в Web UI: способы на выбор — одноразовая ссылка, пароль (argon2id), подтверждение в Telegram-боте; настройки — kv `ui.login` State Store, не в конфигурации; с паролем/Telegram ссылка даёт только попытку входа; сброс `gate4ai daemon --reset-ui-login` | `internal/front/webui/protect.go` (тесты `protect_test.go`), `internal/approval/telegram.go` (`ConfirmLogin`), `cmd/gate4ai/setup.go` |
| Автоподключение клиентов MCP (Claude Code, Claude Desktop, Cursor, VS Code, Codex CLI, Gemini CLI) | `internal/front/agentsetup` |
| Структурные правки конфигурации, модель правок `gate4ai.change_policy` | `internal/config/edit.go`, `internal/config/change.go` |
| Самонастройка: встроенный коннектор `gate4ai` (`change_policy` — класс write под обычной политикой, `policy_summary`); проверка и текст запроса подтверждения — хук `Pipeline.Describe` | `internal/daemon/selfconfig.go` |
| Каналы подтверждений для Web UI: состояние, статистика, мастер Telegram; браузерный push (Web Push, VAPID, RFC 8291) | `internal/approval/channels.go`, `internal/approval/webpush.go` |
| Автозапуск (systemd --user / XDG, LaunchAgent, HKCU Run) — общий для `gate4ai service` и Web UI | `internal/platform/autostart` |
| Трей (бинарник `gate4ai-tray`) | `cmd/gate4ai-tray` (меню, fyne.io/systray), `internal/front/tray` (состояние, решения, уведомления) |
| Магазин: пакеты .g4c, индекс registry, установка с согласием, отзыв | `internal/connectors/store`, `registry/`; формат индекса и его подпись — `sdk-go/registry` (те же правила у сайта gate4.ai) |
| Изоляция: Egress Proxy, `gate4ai run`, `doctor` | `internal/hostsvc/egress`, `internal/sandbox` |
| Сквозной тест (хост, relay и настоящие коннекторы из того же коммита) | `e2e/` |
| CI, направление зависимостей, SPDX, проверка истории на секреты; выпуск — [publishing.md](publishing.md) | `scripts/ci.sh`, `scripts/deps-check.sh`, `scripts/spdx.sh`, `scripts/secret-scan.sh`, `.github/workflows/` |
