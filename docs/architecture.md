# Устройство кода

| Компонент README | Пакет |
|---|---|
| Модель конфигурации, Config Store (версии, откат, права владельца) | `internal/config` |
| Policy Engine (CEL, deny > ask > allow, умолчания по классу, лимиты) | `internal/policy` |
| Action Pipeline (8 шагов) | `internal/pipeline` |
| State Store (счётчики, очередь подтверждений, метки происхождения, токены) | `internal/state` |
| Журнал: квитанции RFC 8785, цепочка, AEAD, доказательства age, чекпоинты, экспорт | `internal/audit` |
| Vault (keychain, алиасы, маскирование) | `internal/secrets/vault`, `internal/secrets/keys` |
| Approval Service: desktop, Telegram | `internal/approval` |
| Манифест, ABI v1, функции хоста, проверка возможностей, PDK и тестовый хост коннекторов | `sdk-go/` (`github.com/gate4ai/sdk-go`, Apache-2.0) |
| Реестр коннекторов, WASM-хост (wazero), установленные коннекторы | `internal/connectors`, `internal/connectors/wasm`, `internal/connectors/store` |
| Браузерный воркер (Chrome, CDP, Stagehand, Executor) — служба хоста | `internal/hostsvc/browser` |
| Коннекторы (WASM-модули, AGPL-3.0 + исключение): почта, файлы, браузер | `connectors/mail`, `connectors/files`, `connectors/browser` (`github.com/gate4ai/gate4ai-*`) |
| daemon, API на сокетах владельца и агентов | `internal/daemon`, `internal/platform/ipc` |
| CLI, skill | `internal/front/cli`, `internal/front/skill` |
| MCP Front (stdio, Streamable HTTP), OAuth 2.1 | `internal/front/mcp`, `internal/front/mcpcli`, `internal/front/oauth` |
| Relay — сервер (туннель, passthrough, DNS/ACME, свидетель), отдельный модуль | `relay/` (`github.com/gate4ai/relay`), `relay/cmd/gate4ai-relay` |
| Relay — клиент устройства | `internal/relayclient` |
| Web UI | `internal/front/webui` |
| Трей | `tray/` (отдельный репозиторий `gate4ai/tray`) |
| Магазин: пакеты .g4c, индекс registry, установка с согласием, отзыв | `internal/connectors/store`, `registry/` |
| Изоляция: Egress Proxy, `gate4ai run`, `doctor` | `internal/hostsvc/egress`, `internal/sandbox` |
| Сквозной тест | `e2e/` |
| Разделение на репозитории, проверка истории на секреты | `scripts/split.sh`, `scripts/secret-scan.sh`, [publishing.md](publishing.md) |
