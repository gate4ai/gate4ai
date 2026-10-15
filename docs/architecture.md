# Устройство кода

| Компонент [спецификации](specification.md) | Пакет |
|---|---|
| Модель конфигурации, Config Store (версии, откат, права владельца) | `internal/config` |
| Policy Engine (CEL, deny > ask > allow, умолчания по классу, лимиты) | `internal/policy` |
| Action Pipeline (8 шагов) | `internal/pipeline` |
| State Store (счётчики, очередь подтверждений, метки происхождения, токены) | `internal/state` |
| Журнал: квитанции RFC 8785, цепочка, AEAD, доказательства age, чекпоинты, экспорт | `internal/audit` |
| Vault (keychain, алиасы, маскирование) | `internal/secrets/vault`, `internal/secrets/keys` |
| Approval Service: desktop, Telegram | `internal/approval` |
| Манифест, ABI v2, функции хоста, проверка возможностей, PDK и тестовый хост коннекторов | `sdk-go/` (`github.com/gate4ai/sdk-go`, Apache-2.0) |
| Реестр коннекторов, WASM-хост (wazero), установленные коннекторы | `internal/connectors`, `internal/connectors/wasm`, `internal/connectors/store` |
| Браузерный воркер (Chrome, CDP, Stagehand, Executor) — служба хоста | `internal/hostsvc/browser` |
| Коннекторы (WASM-модули, AGPL-3.0 + исключение): почта, файлы, браузер | `connectors/mail`, `connectors/files`, `connectors/browser` (`github.com/gate4ai/gate4ai-*`) |
| daemon, API на сокетах владельца и агентов | `internal/daemon`, `internal/platform/ipc` |
| Публичный адрес устройства: один обработчик для relay и прямого HTTPS (`--public`), разделение по путям — `/mcp` и OAuth, `/v1/{whoami,tools,call,status}` (удалённый CLI), `/ui/` (удалённый Web UI), `/telegram` (вебхук); у каждого пути своя аутентификация, API владельца нет | `internal/daemon` (`PublicHandler`, `AddPublic`, `ServePublic`), `cmd/gate4ai/setup.go`, тест изоляции `cmd/gate4ai/public_test.go` |
| CLI, skill | `internal/front/cli`, `internal/front/skill`; ошибки для человека и агента (`hint`, `detail`) — `errors.go`; `gate4ai daemon start/stop/status` — `daemonctl.go`; автозапуск `gate4ai service` (systemd --user / XDG autostart, launchd, HKCU Run) — `service*.go`; UX путей пользователя — [cli-ux.md](cli-ux.md) |
| MCP Front (stdio, Streamable HTTP), OAuth 2.1 | `internal/front/mcp`, `internal/front/mcpcli`, `internal/front/oauth` |
| Relay — сервер (туннель, passthrough, DNS/ACME, свидетель), отдельный модуль | `relay/` (`github.com/gate4ai/relay`), `relay/cmd/gate4ai-relay` |
| Relay — клиент устройства | `internal/relayclient` |
| Web UI | `internal/front/webui` |
| Трей (бинарник `gate4ai-tray`) | `cmd/gate4ai-tray` (меню, fyne.io/systray), `internal/front/tray` (состояние, решения, уведомления) |
| Магазин: пакеты .g4c, индекс registry, установка с согласием, отзыв | `internal/connectors/store`, `registry/` |
| Изоляция: Egress Proxy, `gate4ai run`, `doctor` | `internal/hostsvc/egress`, `internal/sandbox` |
| Сквозной тест (хост + настоящие коннекторы) | отдельный репозиторий `gate4ai/e2e` |
| Разделение на репозитории, проверка истории на секреты | `scripts/split.sh`, `scripts/secret-scan.sh`, [publishing.md](publishing.md) |
