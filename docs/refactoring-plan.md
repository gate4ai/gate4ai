# План рефакторинга gate4ai

Составлен 2026-10-24 по итогам чтения кода (ядро, CLI, Web UI, каналы подтверждений, pipeline, браузерный коннектор, `sdk-go`, хосты коннекторов, config/state). Прежние ревью: «Код-ревью проекта» и «Ревью кода на упрощение» (коммит 8528f78) убрали дубли и обёртки; этот план — про структуру, которую те ревью не трогали. Устройство кода — [architecture.md](architecture.md); каждый пункт ниже привязан к файлам и строкам на момент составления.

## Принципы

- **Поведение не меняется.** Исключение — этап 0: это дефекты, их исправление меняет поведение намеренно и описано отдельно.
- **Сначала тест, потом перенос.** Логику, которую выносим из хендлера или большого `switch`, прежде покрываем юнит-тестом на месте; переносим под зелёными тестами.
- **Маленькие шаги.** Один пункт плана — одна задача pitwall и одна ветка; каждая проходит `./scripts/ci.sh` (на этапах, трогающих браузер и relay, — и `./scripts/ci.sh e2e`).
- **Границы остаются.** Направление зависимостей из `scripts/deps-check.sh` не нарушаем; новые общие пакеты — листовые (не импортируют ядро).
- **Документация вместе с кодом.** Переезд пакета или смена контракта — правка `architecture.md` в том же коммите. Спецификацию этот план не меняет.

Приоритеты: **P0** — дефект, **P1** — большая отдача за небольшой риск, **P2** — полезно, можно отложить, **P3** — по желанию.

## Этап 0. Дефекты, найденные при исследовании (P0)

| # | Что | Где | Исправление | Проверка |
|---|---|---|---|---|
| 0.1 | Две копии маскирования секретных параметров URL разошлись: регэксп `secretParam` скопирован, но `pipeline.maskURL` не разбирает SPA-фрагмент (`#/cb?code=…` → ключ `/cb?code`, код не маскируется), а `worker.RedactURL` разбирает. Pipeline — последний рубеж маскирования журнала и ответа агенту | `internal/pipeline/urlmask.go:18,44-59`, `connectors/browser/worker/explore.go:582` | Одна реализация в `sdk-go/domain` (или новом `sdk-go/redact`), обе стороны её импортируют | Тест на `#/cb?code=…` в `urlmask_test.go`; удалённый дубль |
| 0.2 | Встроенный коннектор не получает ctx вызова: `connector.Handle` вместо `HandleContext`; дедлайн и отмена до кода `files` не доходят | `internal/connectors/wasm/builtin.go:52` | `HandleContext(ctx, …)` | Тест: отменённый ctx прерывает долгий вызов builtin |
| 0.3 | WASM: Dispatcher получает ctx без дедлайна, тайм-аут ставится позже в `invoke`; хост-вызов (`http_request`) может жить ещё `TimeoutS` после дедлайна модуля | `internal/connectors/wasm/wasm.go:253,342` | Тайм-аут до создания Dispatcher, как у native и builtin | Тест с медленным фейковым HTTP |
| 0.4 | State Store без миграций: только `CREATE TABLE IF NOT EXISTS`; новые данные добавлялись боковыми таблицами (`token_dirs`, `approval_callers`, `token_seen`), колонки с `DEFAULT` в старой базе не появятся | `internal/state/state.go:36-56`, `ui.go:13-21`, `pairing.go:22-30` | `PRAGMA user_version` и список миграций по образцу `internal/audit/schema.go` (append-only, тест как `TestMigrationsAppendOnly`) | Открытие базы, созданной прежней схемой (фикстура) |
| 0.5 | При установке коннектора с тем же именем от другого издателя ошибка `claimData` только логируется; новый коннектор может прочитать kv прежнего (токены) | `internal/connectors/store/install.go:464-466` | Ошибка установки | Тест: нечитаемая папка данных → установка не проходит |
| 0.6 | JSON-RPC `ID *int64` в двух местах; строковый id (разрешён в JSON-RPC и MCP) не разбирается — parse error без id | `internal/platform/rpc/rpc.go:21`, `sdk-go/native/native.go:86`, `internal/front/mcp/mcp.go:296` | `json.RawMessage` для id; один сборщик ответа вместо копии в `mcp.HandleMessage` (разные коды по умолчанию: `CodeInternal` vs `CodeApp`) | Тест MCP с `"id":"abc"` |
| 0.7 | В корень репозитория закоммичен бинарник `browser` (14 МБ, ELF; коммит 6c6f05c) | `gate4ai/browser` | `git rm`, `/browser` в `.gitignore`; историю не переписывать | `git ls-files browser` пуст |
| 0.8 | `reveal` в native-хосте копирует `Dispatcher.secret` и не записывает значение в `d.used` — `Dispatcher.Mask` не маскирует раскрытые значения | `internal/connectors/native/native.go:272-284`, `sdk-go/hostfuncs/hostfuncs.go:125-131` | Метод `Dispatcher.Reveal(alias)` | Тест: раскрытое значение в stderr маскируется |
| 0.9 | Тело запроса читается `json.NewDecoder(r.Body)` мимо `readJSON` (лимит 16 МиБ, `UseNumber`) | `internal/daemon/connectors.go:251,262,277` | `readJSON` | Существующие тесты |
| 0.10 | `closeSession`: если замок не взят за `AgentWait`, `Release` выполняется, пока действие ещё идёт (`Executor.Sites`, `LoginOrigins`, `Session.done` без синхронизации) — вероятная гонка | `connectors/browser/sessions.go:373-381` | Освобождение после завершения действия (или `sync.Once` на `finish`) | `go test -race` с закрытием во время действия |

## Этап 1. Daemon и CLI (P1)

Цель: убрать ручную маршрутизацию и дубли по обе стороны API, разорвать лишнюю зависимость CLI от ядра.

1.1 **`Daemon.Handler` (367 строк, `internal/daemon/api.go:260-626`) → группы маршрутов** по образцу уже существующих `connectorRoutes`, `pairRoutes`, `agentRoutes`: `journalRoutes`, `policyRoutes`, `agentAdminRoutes`, `approvalRoutes`, `vaultRoutes`, `siteRoutes`. Бизнес-логику из замыканий (фильтр родителей в `/v1/log` 320-343, валидация sites 382-391, правило трея 557-564) — в именованные методы. Проверка: `TestDaemonSockets` зелёный; перечень маршрутов до и после совпадает (снять список и сравнить).

1.2 **Общий JSON-хендлер и один owner-guard.** Сейчас 39 `WriteError` + 11 `readJSON` в одном файле и три проверки владельца с разными сообщениями (`own` api.go:272, `owner` connectors.go:179, `sandbox/attach.go:209`). Ввести `jsonHandler[Req](func(r, p, Req) (any, *apiErr))` и один guard. Заодно: единый парсер `since` (`/v1/log` — `config.ParseDuration`, молча глотает ошибку; `/v1/connectors/logs` — `time.ParseDuration`, без `7d`, 400) и конверт ошибки `{status:"error"}` в ответе 409 с планом согласия (`connectors.go:235`) через `WriteErrorWith(..., extra)`.

1.3 **Таблица команд CLI.** Пять согласуемых вручную списков (`commands` cli.go:213, `switch cmd` 249-310, `agentCommands` 318, `tableCommands` output.go:24, `knownConnectors` 453) и 12 `switch sub` с собственными строками `usage:` заменить одной таблицей `map[string]command{run, agent, table, offline, usage, subs}`; `commandHelp` берёт текст из таблицы, а не грепает `usage`. Проверка: `TestCLIFlow` и тесты `suggest`/`Explain`; снимок `gate4ai help` и всех `usage:` до и после.

1.4 **Разрыв `cli → sandbox → daemon`.** `cli/run.go` импортирует `internal/sandbox`, а `sandbox/attach.go:25` — `internal/daemon`; `go list -deps ./internal/front/cli` тянет daemon, pipeline, policy, state, audit, oauth, mcp, egress. Перенести `Attach`/`Runs` в `internal/sandbox/attach` (или в daemon); `cli/agentkey.go:19` не должен импортировать `internal/state` ради констант `Pending/Approved/Rejected` и `HashToken`. Проверка: `go list -deps ./internal/front/cli | grep internal/daemon` пуст; добавить это правило в `scripts/deps-check.sh`.

1.5 **Листовой wire-пакет** (`internal/platform/apiwire`): конверт ответа, `APIError` (сейчас `daemon.APIError` и `cli.APIError`), заголовки (`ClientHeader`/`DirHeader` против литералов в `client.go:112-114`), `Tool` (`daemon.Tool` ⊃ `skill.Tool`), константы путей `/v1/…` (≈57 литералов в cli, mcpcli, tray), общая `filterOneShot`/`withoutOneShot` (две идентичные функции: `cli/daemonctl.go:306`, `daemon/api.go:746`).

1.6 **Разбиение файлов без смены кода:** `cli/cli.go` (1413) → `actions.go`, `save.go`, `journal_cmds.go`, `policy_cmds.go`, `owner_cmds.go`, `ui.go`, `suggest.go`; из `cli/connectors.go` (780) — издательский инструментарий (`pack`, `verify`, `keygen`, `registry`) в `publish.go`/`registry.go`. Один argv-сплиттер вместо пяти (`splitFlags`, `firstPositional`+`isBoolFlag`, `hoistCallOpts`, `stripJSONFlag`, `flagValue`) на основе `flag.Value.IsBoolFlag()`.

1.7 Мелочи попутно: `Env.run` (`run.go:55-179`) → `maskDryRun`, `writeSpecFiles`, `keepAlive`; `--container --microvm` вместе — ошибка, а не молчаливый microvm; `oneLine` в CLI считает байты и режет UTF-8 (`output.go:209`) — считать руны, как в `selfconfig.go:176`; в `Env.approvals` два подряд `switch sub` (1189, 1216) слить.

1.8 **Тесты:** `TestCLIFlow` (149 строк, 35 команд в одном сценарии) и `TestDaemonSockets` (151) — в табличные `t.Run` на общем харнесе (харнес `startWith` уже хорош). Добавить тесты уровня daemon на `/v1/vault*`, `/v1/export`, `/v1/show`, `/v1/policy/versions`, `/v1/pairings`, `/v1/agents/{name}/connect` и на установку коннектора с фейковым `.g4c` (сейчас только в e2e).

## Этап 2. Pipeline и браузерный коннектор (P1)

2.1 **Таблица действий в браузерном коннекторе.** Знание о каждом действии размазано по шести `switch` и манифесту: `Execute` (`browser.go:255-278`), `agent` (316-347), `page` (410-423, 435-441), `callSession.do` (471-695, 225 строк), `actionTimeout` (`sessions.go:42-75`), `record` (708). Запросы разбираются дважды (`fetchRequest`, `downloadRequest`, `handoffRequest`, `actRequest`). Ввести `map[string]actionSpec{check, parse, timeout, agentOnly, dialogPassthrough, navigates, loginHint, run}`; тайм-ауты по умолчанию (`10s`/`5s` в `sessions.go:56,60` дублируют `engine.go:323,508`) — общие константы. Непроверяемые `res.Structured.(map[string]any)` (`browser.go:439`, `sessions.go:238`) — через `, ok`. Защита: 22 теста `browser_test.go`.

2.2 **`Pipeline.execute` (`pipeline.go:434-599`, 166 строк) → фазы:** квитанция и намерение; `invoke` и классификация ошибки; проверка схемы, доказательства, taint; верификация; финальная запись и fail-closed. Пять одинаковых блоков «статус квитанции → `appendResult` → `res.Status/Code/ID/Receipt/Message`» — один `errResult`. Дубль browser-precheck в `invoke` (642-653 и 654-664) — один. Защита: `order_test.go` (повторная проверка при одобрении, намерение до исполнения, отказ журнала = отказ действия, восстановление).

2.3 **`loginVia` через путь `ExecuteNested`.** `credentials.go:366-389` и `nested.go:66-117` повторяют цепочку глубина → `Normalize` → решение → `deny` → `run` → `abi.Error`; при этом `loginVia` вызывает `Engine().Decide` напрямую (`credentials.go:373`), минуя `provenance`, `StateError` и fail-closed-логирование `decidePolicy`. Свести к одной функции. Проверка: тест, что отказ журнала при `loginVia` останавливает вход.

2.4 **Явная сборка Pipeline.** `pipeline.New` принимает 6 аргументов, затем `daemon.go:184-196` присваивает ещё 8 полей (`Vault` — дважды); `Describe`/`AlwaysAsk` — карты с единственной записью (`SelfConnector`). Ввести `pipeline.Hooks` (или `Options`) в `New`; карты заменить интерфейсом `HostConnector`.

2.5 **Брокер учёток отдельно от Pipeline (P2).** Ядро знает протокол коннектора `browser` (`BrowserConnector`, `ArgPrecheck`, `ArgRecordOnly`, имена аргументов `session/message/account/field/source` в `invoke` 630-681 и `credentials.go`). Вынести `credentials.go` и browser-ветки `invoke` в тип `credentialBroker` с узкими зависимостями (cfg, Registry, Vault, Ask, Journal, callback `run`). `waiters` подтверждений — в свой тип со своим мьютексом (сейчас делят `p.mu` с `engine`).

2.6 **Worker: повторяющиеся куски.** Эпилог «ушли с разрешённых сайтов» (`settledURL` → `about:blank` при `ErrDomain` → `HookNetwork`) — 5 копий (`engine.go:121-130, 369-377`, `dialog.go:231-239`, `handoff.go:82-90`, `login.go:351-355`, где вместо `settledURL` — `Engine.URL`+`CheckURL`) → `landed()`. Циклы ожидания с дедлайном — 8 копий (`engine.go:95,330,511`, `handoff.go:57`, `login.go:327`, `download.go:171`, `wait.go:122`, `cdpnet.go:152`) → `poll(ctx, timeout, step, cond)`. CDP: `getResponseBody`+base64 — 3 копии (`cdpnet.go:122,184,277`), dial+`readLoop` — 2 (`cdp.go:100-118, 177-189`). JS-предикат `vis` — ~9 вставок (`engine.go`, `login.go`, `changes.go`, `explore.go`) → константа рядом с `visibleTextJS`.

2.7 **Блокировки worker (P1, по одной задаче на пункт):**
- `Service.executor` под `s.mu` делает ping Chrome (до 3 с), dial CDP, запуск/остановку Chrome; пока идёт — блокируются `release()` всех сессий, `Gone()`, `downloadsConn()` (`service.go:264-267, 351-354`). Вынести I/O из-под замка за флаг «инициализируется».
- `Browser.session` держит глобальный `b.create` через `W.Acquire` с неограниченным ожиданием (`browser.go:352-374`) — сериализует создание первой страницы для несвязанных вызовов. Замок на ключ (singleflight).
- `Downloads.begin` берёт `d.busy.Lock()` без ctx (`download.go:559`) — вторая загрузка висит дольше своего дедлайна, сессия остаётся `session_blocked`. Ожидание с ctx.
- `Downloads.event` на read loop CDP делает файловый I/O (`moveFile`, `RemoveAll`; `download.go:444-503`) — стопорит ответы соединения. Перенести в горутину.
- Таймер `SessionIdle` страницы коннектора сбрасывается только в `session()` (`browser.go:356`): долгий сервисный вызов (>2 мин) может вызвать `EndCall` посреди корневого вызова. Подтвердить тестом, затем сбрасывать на каждом действии.

2.8 **Тесты браузера.** `fakeEngine.Eval` выбирает ответ по подстрокам JS (`browser_test.go:79-139`, ~20 веток; переименование переменной в скрипте молча уводит в ответ по умолчанию) — типизированная диспетчеризация. Chrome-тесты повторяют `FindChrome`-настройку — общий `chromeServer` из `edit_chrome_test.go:55`. Недостающие: `-race` на закрытие во время действия, две конкурирующие загрузки.

## Этап 3. Web UI и каналы подтверждений (P1–P2)

3.1 **Доменная логика из хендлеров (P1).** `setupSave` (`setup.go:329`, 122 строки) смешивает разбор формы, сборку политики в замыкании `u.edit` (профиль, коннекторы, правила по группам, папки `files`, домены `browser`), побочные эффекты (автозапуск, автоподключение, relay) и сообщение. Сборку политики — в чистую функцию `config.ApplySetup(c, …)` с юнит-тестами без HTTP. Последовательность «id → проверка дубля → включить коннектор профиля → `PutRule`» повторяется в `saveRule` (`policypages.go:325-340`), `allowAlways` (`approvals.go:300-309`), `setupSave` (368-369) → `upsertRule(c, prof, rule)`. `storeItems` (`connpages.go:39`, 98 строк; GET страницы может пойти в сеть за индексом) — разделить на выборку и чистое слияние.

3.2 **Локализация каналов подтверждений (P1).** Desktop (`approval/approval.go:142-179`), Telegram (`telegram.go`: 212, 232-245, 283-287, 301-335, 361, 372, 408-413, 520-616) и webpush (`webpush.go:90-111`, одно сообщение на двух языках) — русский в коде, тогда как Web UI двуязычный. Вынести `Msg`/`tr`/каталог из `webui/i18n.go` в `internal/i18n`, каналам передавать язык. Перевод по английскому тексту ядра (регэкспы `reasons` в `approvals.go:105-129`, `tr(l, err.Error())`, `errview.go`) заменить кодами причин из `policy`/`pipeline` — иначе смена формулировки в ядре молча ломает перевод. Тест полноты `i18n_test.go` (регэксп по исходникам + 12 таблиц вручную) после этого упростится.

3.3 **Типизированный результат хендлера (P2).** 22 page-хендлера и 44 POST-хендлера возвращают `map[string]any` с управляющими ключами `redirect/page/flash/error/detail/status` (`webui.go:653-719`); опечатка видна только в рантайме. Небольшая структура `result{Redirect, Page, Flash, Err, Detail, Status, Data}`; типизированные view-model по страницам — по желанию.

3.4 **Узкие интерфейсы вместо `*daemon.Daemon` (P2).** `UI.D` используется 160+ раз (≈50 членов), в основном поля: `D.Config.Current` (35), `D.PublicURL` (14), `D.Registry.Get` (10), `D.State.*`, `D.Journal.*`, `D.Vault.*`. Та же картина в `approval.Channels` (`channels.go:17`, `attach.go:104`) — поэтому `approval` импортирует `daemon`. Путь: webui и `daemon/api.go` (владельческий API, маршруты 281-699, делает те же операции) вызывают методы daemon, а не лезут в хранилища; затем интерфейсы по областям (approvals, agents, policy/config, vault, connectors). Единым «толстым» интерфейсом не заменять.

3.5 **Дубли в каналах (P2).** `pairCallback`/`loginCallback` (`telegram.go:225-246`, `276-290`) и `PairRequested`/`ConfirmLogin` — один обобщённый «запрос с кнопками»; текст для `ErrAlreadyDecided`/`ErrExpired` — трижды (`telegram.go:236-243, 605-614`, `approvals.go:236-247`); сводка действия — дважды с разными ключами (`approval.Summary` с маскированием, `webui argTarget/argString` без); `Channels.Test` для Telegram создаёт новый `BotAPI` вместо работающего экземпляра.

3.6 **P3:** партиал `{{template "csrf"}}` вместо 71 ручного поля; 63 inline `style=` — в `_layout.html`; `Channels.Status` — метод канала; тесты на маршруты без покрытия (`/connectors/install`, `/policy/lists`, `/policy/rule/move`, `/agents/{name}/delete`, `/push/subscribe`, `/settings/*`, `/setup/code`, `/channels/{name}/test`, `/vault/delete`).

## Этап 4. Хосты коннекторов, sdk-go, хранилища (P2)

4.1 **Один цикл вызова коннектора.** Реализация функций хоста одна (`sdk-go/hostfuncs`), но обвязка вызова (лимиты, тайм-аут, Dispatcher, `Close`, marshal/decode, `Evidence`) скопирована четырежды: `wasm.go:341-368`, `builtin.go:41-66`, `native.go:221-303`, `hosttest.go:75-130`; отображение ошибок с одинаковым текстом — `wasm.go:297-309` и `native.go:251-258`. Общие `runCall(ctx, man, settings, func(d) error)` и `mapCallErr`.

4.2 **Dispatcher-фабрика вне `wasm.Engine`.** `wasm.Engine` одновременно среда wazero и фабрика хост-сервисов (`wasm.go:33-49, 313-334`); поэтому `native` импортирует `wasm` (`native.go:32`) ради `Hosts` и `Toolchain` (`:416`), а CLI — `wasm.Toolchain` для нативных сборок (`cli/connectors.go:485,552,624`). Разнести: `internal/connectors/hostsvc` (Services + Dispatcher) и `internal/connectors/build` (Toolchain, BuildGo). Обновить устаревший doc-комментарий `host.go:3-6` (store-коннекторы «только WASM», egress назван Builtin).

4.3 **Одна проверка имени коннектора.** Четыре регэкспа, три разных: `manifest.go:262`, `config/credentials.go:120` (тот же), `config/load.go:123` (`^[a-z0-9_]+$`, компилируется в цикле), `store/install.go:493` (допускает ведущую цифру и `_`). Экспортировать `manifest.ValidName`. Там же: дубль проверки диапазона портов (`capabilities.go:177-181` и `Resolve` 415-420), `store.SHA256` = `registry.Sum`.

4.4 **Один JSON-RPC по строкам.** `internal/platform/rpc/rpc.go` и `sdk-go/native/native.go:84-290` (`Peer`) — побайтно одинаковые `Message`; ядру можно импортировать `sdk-go`, `rpc` переиспользует `native`. Делается вместе с 0.6.

4.5 **State kv и OAuth.** `state/oauth.go:44-64` знает формат ключа `oauth.client.*` и JSON-форму (`json_extract` с join на `tokens.label`) — утечка слоя; типизированная таблица `oauth_clients` или перенос в `front/oauth`. Делается после 0.4 (миграции).

4.6 **Сборка процесса.** Флаги `cmd/gate4ai/setup.go:25-37` — пакетные глобалы (`uiAddr`, `resetUILogin`, `telegramAPI`, `egressAddr`, `public`), `setup()` без теста → структура `setupOpts`. `daemon.New` (`daemon.go:111-213`) при ошибке после открытия Vault/Wasm не закрывает `Wasm`, `Registry`, `State` → отложенное закрытие при ошибке; шаги загрузки назвать (`loadConnectors`, `wirePipeline`), порядок оставить линейным (Config нужен Registry, State — после policy).

4.7 **`Daemon` как service locator (P2–P3).** 31 поле, ~70 методов в 12 файлах, один `d.mu` на несвязанное (`policyWarn`, `startErrs`, `servers`, `stop`, `oauth`, `public*`). Выделять по мере надобности: публичную поверхность (`public`, `publicURLs`, `publicMux`, `oauth`, `PublicHandler`, `buildPublic`, `backend` — свой мьютекс вместо танца в `public.go:140-156`), компилятор политики (`strict`, `startErrs`, `policyWarn`, `validate`, `compilePolicy`), менеджер установки (`connectors.go`, `defaults.go`, `installMu`), жизненный цикл серверов. MCP-over-HTTP хендлер и адаптер `backend` (`public.go:28-63, 221-250`) логичнее в `front/mcp` за узким интерфейсом — сейчас ядро импортирует `front/mcp` и `front/oauth`, а `front/webui` импортирует ядро. `selfconfig.go`+`selfpin.go`+правило трея — кандидат в подпакет, не срочно. Экспортированные поля `Daemon` оставить, пока не сделан 3.4.

## Что оставить как есть

Таблица маршрутов `webui.handler`, шаблоны и партиалы `head`/`foot`, наборы шаблонов по языкам, дизайн `Msg`/`Word`, middleware безопасности (`guard`, `post`, `originOK`), `switchable`/`Attach` каналов, desktop-уведомления (защищены от инъекций); `Client.Do`, `errors.go` CLI, таблицы `output.go`, `pairing.go`, `services.go`, `questions.go`, `handoff.go`, `connlog.go`; `recover.go`, `approval.go` pipeline (кроме `waiters`), проверки `nested.go`; `cdp.go` `call`/`readLoop`, `dialog.go`, `reserve`/`release`, пробинг необязательных интерфейсов `Engine`; дизайн `hostfuncs`/`capability`, форма валидации манифеста, версионирование Config Store, миграции журнала, build-теги платформы. `hostfuncs.go` (772 строки) и `Manifest.Validate` (130) длинные, но связные — делить только косметически.

## Порядок и объём

1. Этап 0 целиком — каждая строка таблицы отдельной задачей, 0.1 и 0.7 первыми (безопасность и размер репозитория).
2. Этап 1: 1.4 (новое правило в `deps-check.sh` не даст откатиться) → 1.5 → 1.1–1.2 → 1.3 → 1.6–1.8.
3. Этап 2: 2.7 (замки) и 2.1 параллельно, затем 2.2–2.3, далее 2.4–2.6, 2.8.
4. Этап 3: 3.1 и 3.2, затем 3.3–3.5 по мере правок соответствующих страниц.
5. Этап 4 — по мере касания кода; 4.4 и 4.5 привязаны к 0.6 и 0.4.

Критерий готовности этапа: все пункты либо сделаны, либо явно отклонены с причиной в этом документе; `architecture.md` отражает новые пакеты; `./scripts/ci.sh` и `./scripts/ci.sh e2e` зелёные.
