# План рефакторинга gate4ai

> Этап 0 выполнен 2026-10-24 (коммиты после 91c4e95). Этапы 1–4 — технический долг: [tech-debt.md](tech-debt.md).

Составлен 2026-10-24 по итогам чтения кода (ядро, CLI, Web UI, каналы подтверждений, pipeline, браузерный коннектор, `sdk-go`, хосты коннекторов, config/state). Прежние ревью: «Код-ревью проекта» и «Ревью кода на упрощение» (коммит 8528f78) убрали дубли и обёртки; этот план — про структуру, которую те ревью не трогали. Устройство кода — [architecture.md](architecture.md); каждый пункт ниже привязан к файлам и строкам на момент составления.

## Принципы

- **Поведение не меняется.** Исключение — этап 0: это дефекты, их исправление меняет поведение намеренно и описано отдельно.
- **Сначала тест, потом перенос.** Логику, которую выносим из хендлера или большого `switch`, прежде покрываем юнит-тестом на месте; переносим под зелёными тестами.
- **Маленькие шаги.** Один пункт плана — одна задача pitwall и одна ветка; каждая проходит `./scripts/ci.sh` (на этапах, трогающих браузер и relay, — и `./scripts/ci.sh e2e`).
- **Границы остаются.** Направление зависимостей из `scripts/deps-check.sh` не нарушаем; новые общие пакеты — листовые (не импортируют ядро).
- **Документация вместе с кодом.** Переезд пакета или смена контракта — правка `architecture.md` в том же коммите. Спецификацию этот план не меняет.

Приоритеты: **P0** — дефект, **P1** — большая отдача за небольшой риск, **P2** — полезно, можно отложить, **P3** — по желанию.

## Этап 0. Дефекты, найденные при исследовании (P0) — выполнено

| # | Что | Где | Исправление | Проверка |
|---|---|---|---|---|
| 0.1 | Две копии маскирования секретных параметров URL разошлись: регэксп `secretParam` скопирован, но `pipeline.maskURL` не разбирает SPA-фрагмент (`#/cb?code=…` → ключ `/cb?code`, код не маскируется), а `worker.RedactURL` разбирает. Pipeline — последний рубеж маскирования журнала и ответа агенту | `internal/pipeline/urlmask.go:18,44-59`, `internal/connectors/builtin/browser/worker/explore.go:582` | Одна реализация в `sdk-go/domain` (или новом `sdk-go/redact`), обе стороны её импортируют | Тест на `#/cb?code=…` в `urlmask_test.go`; удалённый дубль |
| 0.2 | Встроенный коннектор не получает ctx вызова: `connector.Handle` вместо `HandleContext`; дедлайн и отмена до кода `files` не доходят | `internal/connectors/wasm/builtin.go:52` | `HandleContext(ctx, …)` | Тест: отменённый ctx прерывает долгий вызов builtin |
| 0.3 | WASM: Dispatcher получает ctx без дедлайна, тайм-аут ставится позже в `invoke`; хост-вызов (`http_request`) может жить ещё `TimeoutS` после дедлайна модуля | `internal/connectors/wasm/wasm.go:253,342` | Тайм-аут до создания Dispatcher, как у native и builtin | Тест с медленным фейковым HTTP |
| 0.4 | State Store без миграций: только `CREATE TABLE IF NOT EXISTS`; новые данные добавлялись боковыми таблицами (`token_dirs`, `approval_callers`, `token_seen`), колонки с `DEFAULT` в старой базе не появятся | `internal/state/state.go:36-56`, `ui.go:13-21`, `pairing.go:22-30` | `PRAGMA user_version` и список миграций по образцу `internal/audit/schema.go` (append-only, тест как `TestMigrationsAppendOnly`) | Открытие базы, созданной прежней схемой (фикстура) |
| 0.5 | При установке коннектора с тем же именем от другого издателя ошибка `claimData` только логируется; новый коннектор может прочитать kv прежнего (токены) | `internal/connectors/store/install.go:464-466` | Ошибка установки | Тест: нечитаемая папка данных → установка не проходит |
| 0.6 | JSON-RPC `ID *int64` в двух местах; строковый id (разрешён в JSON-RPC и MCP) не разбирается — parse error без id | `internal/platform/rpc/rpc.go:21`, `sdk-go/native/native.go:86`, `internal/front/mcp/mcp.go:296` | `json.RawMessage` для id; один сборщик ответа вместо копии в `mcp.HandleMessage` (разные коды по умолчанию: `CodeInternal` vs `CodeApp`) | Тест MCP с `"id":"abc"` |
| 0.7 | В корень репозитория закоммичен бинарник `browser` (14 МБ, ELF; коммит 6c6f05c) | `gate4ai/browser` | `git rm`, `/browser` в `.gitignore`; историю не переписывать | `git ls-files browser` пуст |
| 0.8 | `reveal` в native-хосте копирует `Dispatcher.secret` и не записывает значение в `d.used` — `Dispatcher.Mask` не маскирует раскрытые значения | `internal/connectors/native/native.go:272-284`, `sdk-go/hostfuncs/hostfuncs.go:125-131` | Метод `Dispatcher.Reveal(alias)` | Тест: раскрытое значение в stderr маскируется |
| 0.9 | Тело запроса читается `json.NewDecoder(r.Body)` мимо `readJSON` (лимит 16 МиБ, `UseNumber`) | `internal/daemon/connectors.go:251,262,277` | `readJSON` | Существующие тесты |
| 0.10 | `closeSession`: если замок не взят за `AgentWait`, `Release` выполняется, пока действие ещё идёт (`Executor.Sites`, `LoginOrigins`, `Session.done` без синхронизации) — вероятная гонка | `internal/connectors/builtin/browser/sessions.go:373-381` | Освобождение после завершения действия (или `sync.Once` на `finish`) | `go test -race` с закрытием во время действия |

Как сделано: 0.1 — пакет `sdk-go/redact`, его используют `pipeline.maskURL` и `worker.RedactURL`; 0.2 — `connector.HandleContext`; 0.3 — тайм-аут в `Connector.run` до создания Dispatcher; 0.4 — `internal/platform/sqlschema` (общий с журналом) и `internal/state/schema.go`, миграция 1 добавляет недостающие колонки с `DEFAULT`; 0.5 — ошибка установки и откат каталога; 0.6 — `rpc.Message.ID` как `json.RawMessage`, один `rpc.Reply` для stdio и HTTP (протокол `sdk-go/native` остаётся с числовыми id — обе стороны наши); 0.7 — `git rm`, `/browser` в `.gitignore`; 0.8 — `Dispatcher.Reveal`; 0.9 — `readJSON`; 0.10 — `closeSession` отвечает сразу, а страницу освобождает горутина после действия (`markClosed` отделён от `closeAgent`).

## Что оставить как есть

Таблица маршрутов `webui.handler`, шаблоны и партиалы `head`/`foot`, наборы шаблонов по языкам, дизайн `Msg`/`Word`, middleware безопасности (`guard`, `post`, `originOK`), `switchable`/`Attach` каналов, desktop-уведомления (защищены от инъекций); `Client.Do`, `errors.go` CLI, таблицы `output.go`, `pairing.go`, `services.go`, `questions.go`, `handoff.go`, `connlog.go`; `recover.go`, `approval.go` pipeline (кроме `waiters`), проверки `nested.go`; `cdp.go` `call`/`readLoop`, `dialog.go`, `reserve`/`release`, пробинг необязательных интерфейсов `Engine`; дизайн `hostfuncs`/`capability`, форма валидации манифеста, версионирование Config Store, миграции журнала, build-теги платформы. `hostfuncs.go` (772 строки) и `Manifest.Validate` (130) длинные, но связные — делить только косметически.

## Порядок и объём (этапы 1–4 — в tech-debt.md)

1. Этап 0 — выполнен (см. выше).
2. Этап 1: 1.4 (новое правило в `deps-check.sh` не даст откатиться) → 1.5 → 1.1–1.2 → 1.3 → 1.6–1.8.
3. Этап 2: 2.7 (замки) и 2.1 параллельно, затем 2.2–2.3, далее 2.4–2.6, 2.8.
4. Этап 3: 3.1 и 3.2, затем 3.3–3.5 по мере правок соответствующих страниц.
5. Этап 4 — по мере касания кода; 4.4 и 4.5 привязаны к 0.6 и 0.4.

Критерий готовности этапа: все пункты либо сделаны, либо явно отклонены с причиной в tech-debt.md; `architecture.md` отражает новые пакеты; `./scripts/ci.sh` и `./scripts/ci.sh e2e` зелёные.
