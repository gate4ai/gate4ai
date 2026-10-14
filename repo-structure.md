# Структура репозиториев и лицензии gate4.ai

> Версия 0.2, 2026-10-13. Лицензии выбраны человеком, переход выполнен (этапы 13–20 PLAN.md): монорепозиторий разделён на модули и готов к разделению на репозитории (`scripts/split.sh`, [docs/publishing.md](docs/publishing.md)). Опирается на [connector-architecture.md](docs/connector-architecture.md).

## Что есть сейчас

Один Go-модуль `github.com/gate4ai/gate4ai`, лицензия AGPL-3.0, три бинарника и 25 пакетов в плоском `internal/`:

| Сейчас | Что внутри | Проблема для разделения |
|---|---|---|
| `cmd/gate4aid` | daemon + режим `gate4aid connector <имя>` | коннекторы mail/files/browser **вкомпилированы в бинарник ядра** → нельзя дать ядру одну лицензию, а коннекторам AGPL |
| `cmd/gate4ai` | CLI + `gate4ai mcp` | два бинарника ядра, а нужен один `gate4ai` |
| `cmd/gate4ai-relay` | сервер relay | — |
| `internal/relay` | **и сервер relay, и клиент устройства** (`attach.go` импортирует `daemon`, `config`, `auth`) | сервер и клиент в одном пакете: relay не отделить, не таща ядро |
| `connectors/browser` | коннектор + CDP + Stagehand + запуск Chrome | браузерный воркер по connector-architecture.md — служба хоста, а не коннектор |
| `connectors/*` → `internal/connector`, `internal/manifest` | SDK коннекторов внутри `internal` ядра | сторонний коннектор не может импортировать `internal/` |
| `connectors/browser` → `internal/policy` | `DomainMatches` из движка политик | коннектор зависит от ядра |
| `tray/` | Tauri (Rust) | другой тулчейн и подпись под каждую ОС (решено иначе: трей переписан на Go и живёт в ядре — `cmd/gate4ai-tray`, 2026-10-14) |

Все сторонние Go-зависимости пермиссивные (Apache-2.0, MIT, BSD, ISC; yamux — MPL-2.0, пофайловый copyleft): ни одна не мешает сменить лицензию ядра.

## Репозитории в github.com/gate4ai

| Репозиторий | Что | Go-модуль | Лицензия |
|---|---|---|---|
| `gate4ai/gate4ai` | ядро: daemon, политики, pipeline, журнал, Vault, подтверждения, MCP, Web UI, Egress Proxy, песочницы, WASM-хост и функции хоста, браузерный воркер, установка коннекторов из магазина, клиент relay. **Бинарник `gate4ai`** и трей `gate4ai-tray` (отдельный бинарник: на macOS трею нужен cgo, ядро собирается без него) | `github.com/gate4ai/gate4ai` | FSL-1.1-ALv2 |
| `gate4ai/sdk-go` | контракт с коннекторами и auth-модулями: схема манифеста, типы ABI v1, обёртки функций хоста для Go (`GOOS=wasip1`), тестовые хосты для unit-тестов коннекторов и auth-модулей | `github.com/gate4ai/sdk-go` | Apache-2.0 |
| `gate4ai/gate4ai-mail` | коннектор почты (WASM) | `github.com/gate4ai/gate4ai-mail` | AGPL-3.0 + исключение для хоста |
| `gate4ai/gate4ai-files` | коннектор файлов (WASM) | `github.com/gate4ai/gate4ai-files` | AGPL-3.0 + исключение |
| `gate4ai/gate4ai-browser` | тонкий коннектор поверх `browser_*` | `github.com/gate4ai/gate4ai-browser` | AGPL-3.0 + исключение |
| `gate4ai/gate4ai-<сервис>` | сервисные коннекторы: `gate4ai-gosuslugi`, `gate4ai-tbank`, `gate4ai-mosenergosbyt` … | `github.com/gate4ai/gate4ai-<сервис>` | AGPL-3.0 + исключение |
| `gate4ai/auth` | наши auth-модули ([secret-architecture.md](docs/secret-architecture.md)): `auth-imap`, `auth-smtp`, `auth-oauth2`, `auth-google-sa`, `auth-totp`, `auth-ssh`, `auth-sigv4`; каждый в своём каталоге со своим `go.mod`, общие тестовые фейки (IMAP / SMTP / SSH / OAuth-серверы) | `github.com/gate4ai/auth/<имя>` | AGPL-3.0 + исключение |
| `<издатель>/gate4ai-auth-<имя>` | auth-модули сторонних издателей (банки, вендоры) | их модуль | AGPL-3.0 + исключение или пермиссивная |
| `gate4ai/relay` | сервер relay (бинарник `gate4ai-relay`); **необязателен** — нужен, только если пользователь не обеспечил HTTPS до daemon сам | `github.com/gate4ai/relay` | FSL-1.1-ALv2 |
| `gate4ai/registry` | индекс магазина: пакеты (`kind: connector \| auth`), версии, sha256, подписи, уровень доверия, список отозванных; шаблоны привязок популярных API (Anthropic, OpenAI, GitHub) | — (данные) | CC0 / Apache-2.0 |
| `gate4ai/.github` | профиль организации, CONTRIBUTING, SECURITY.md, шаблоны issue/PR | — | — |

Имена: ядро и бинарник — `gate4ai`; компоненты — по имени (`relay`, `sdk-go`, `registry`); коннекторы — `gate4ai-<имя>`; auth-модули — `auth-<протокол>` в магазине (`gate4ai/auth-imap`), наши — в одном репозитории `gate4ai/auth`, сторонние — `gate4ai-auth-<имя>`. Auth-модуль — не коннектор: агенту он не виден, действий не имеет, его вызывает ядро для слота учётки. `sdk-go` намеренно без префикса `gate4ai-`, чтобы не путать с коннекторами; под другие языки — `sdk-rust`, `sdk-js`.

### Зависимости между репозиториями

```
          sdk-go  (Apache-2.0: контракт — манифест, ABI v1)
          ▲     ▲
          │     ├──────────────── gate4ai-mail / -files / -browser / -tbank …  (AGPL + исключение)
          │     └──────────────── auth/auth-imap / -oauth2 / -ssh …            (AGPL + исключение)
          │                          ── собираются в .wasm, подписываются, попадают в registry
  gate4ai (FSL) ── скачивает .wasm по registry, проверяет подпись, исполняет в wazero/Extism
          ▲
          └── только в тестах ──▶ relay (FSL)
```

- Ядро **не импортирует** код коннекторов: получает их как `.wasm` (официальные можно встроить в бинарник через `embed` как данные — это поставка рядом, а не линковка).
- Коннекторы и auth-модули импортируют только `sdk-go`; друг от друга не зависят: коннектор ссылается на auth-модуль по имени в манифесте (`module: gate4ai/auth-imap@1`), связывает их ядро.
- Коннекторы импортируют только `sdk-go`. `DomainMatches` и подобные утилиты — в `sdk-go`; проверку доменов всё равно делает хост.
- Relay не импортирует ядро. Клиент relay (регистрация, туннель, DNS-провайдер для ACME) живёт в ядре (`internal/relayclient`). Протокол (HTTP API регистрации, `/tunnel`, свидетель) описан в `relay/docs/protocol.md`. Сквозные тесты «daemon через relay» — в ядре: они импортируют публичный пакет `github.com/gate4ai/relay` (только тесты; лицензии совпадают). Пакеты `internal/` ядра другой модуль импортировать не может, поэтому тест живёт там, где клиент.

## Лицензии

### Ядро: FSL-1.1-ALv2 (Functional Source License)

Ровно то, что нужно: исходник открыт, можно читать, менять, использовать у себя и в компании, **запрещено «конкурирующее использование»** (продукт или сервис, заменяющий gate4ai), и **каждая версия через 2 года автоматически становится Apache-2.0**. Сроки и текст фиксированы, параметр один — лицензия «после»: ALv2 (с патентным грантом, рекомендуется) или MIT. Используют Sentry, Codecov, GitButler.

Альтернатива — BUSL-1.1 (HashiCorp, MariaDB): срок до 4 лет и формулировку ограничения задаёт автор. Гибче, но требует самому написать «Additional Use Grant», и у неё хуже репутация. Для «2 года, без конкурентов» FSL проще.

Важно знать:
- FSL — «fair source», **не open source по OSI**: в Debian/Homebrew core такое не берут, некоторые компании запрещают такие зависимости. Для продукта-приложения (не библиотеки) это обычно не мешает.
- Код, который **должен** свободно встраиваться у других (`sdk-go`), под FSL класть нельзя → Apache-2.0.
- Репозиторий ещё нигде не опубликован под AGPL, поэтому смена лицензии ядра чистая. Если какая-то версия уже была выложена под AGPL — она так и остаётся AGPL у получивших её.
- Чтобы и дальше менять лицензию и выпускать платные версии, нужен **CLA** для внешних контрибьюторов ядра (CLA Assistant в GitHub). Для коннекторов достаточно DCO.

### Коннекторы и auth-модули: AGPL-3.0 + исключение для хоста

Коннектор работает внутри процесса ядра (WASM, вызовы функций хоста). По позиции FSF плагин, обменивающийся с программой структурами данных в одном процессе, может считаться частью одного произведения — тогда AGPL коннектора потребовала бы AGPL от ядра при совместной поставке. Граница WASM + сериализованный ABI делает это спорным, но не нулевым риском.

Снимается дополнительным разрешением по п. 7 AGPL (как GCC Runtime Exception / Classpath Exception) в каждом репозитории коннектора и auth-модулей (auth-модуль исполняется в ядре так же):

> Additional permission under GNU AGPL version 3 section 7: you may combine and distribute this Program, in source or compiled WebAssembly form, together with the gate4ai host (github.com/gate4ai/gate4ai) under that host's license, and execute it inside that host, without the host becoming subject to this License. This permission does not extend to modifications of this Program itself, which remain under the AGPL-3.0.

Правило магазина (`connector-architecture.md`, «лицензия, совместимая с AGPL-3.0, исходник обязателен») дополнить: для уровня «проверен gate4.ai» — AGPL-3.0 с этим исключением или пермиссивная лицензия. То же для auth-модулей; доверие к ним даёт не лицензия, а ревью и уровень в магазине (auth-модули ставятся только с уровнями «проверен gate4.ai» и «проверенный издатель»).

### Остальное

- `sdk-go`: Apache-2.0 — совместима с AGPL коннекторов и с FSL ядра, позволяет и платные коннекторы (README: «поддерживаемые платные коннекторы сервисов»).
- `relay`: FSL-1.1-ALv2, как ядро (relay — потенциальный платный сервис, его не должен хостить конкурент).
- `registry`: CC0 (это данные).
- В каждом файле — SPDX-заголовок (`// SPDX-License-Identifier: FSL-1.1-ALv2`), в корне — `LICENSE` и `NOTICE`.

## Раскладка ядра `gate4ai/gate4ai`

Один бинарник: `gate4ai daemon` (бывший `gate4aid`), `gate4ai mcp`, остальные команды — CLI. Плата: бинарник в песочнице агента тоже станет «полным» (~40 МБ вместо тонкого клиента), но без ключей и конфигурации он ничего не может.

```
cmd/gate4ai/                 main: daemon | mcp | CLI-команды
internal/
  daemon/                    сборка служб в одно целое
  policy/                    PDP: CEL, deny>ask>allow, объяснение
  config/                    модель, загрузка YAML, Config Store с версиями
  pipeline/                  8 шагов Action Pipeline
  state/                     счётчики, очередь подтверждений
  approval/                  Approval Service; каналы: desktop, telegram
  audit/                     квитанции, цепочка, чекпоинты, экспорт/проверка
  secrets/vault, secrets/keys   Vault по алиасам, keychain / файл
  connectors/                реестр коннекторов, вызов execute/verify
    wasm/                    wazero, функции хоста ABI v1, проверка capabilities
    store/                   установка из registry, подпись, согласие, отзыв
  hostsvc/                   службы хоста для коннекторов и агентов
    egress/                  Egress Proxy
    browser/                 браузерный воркер: Chrome-профиль, CDP, Stagehand, Executor
  front/                     то, через что приходят агенты и человек
    cli/  mcp/  mcpcli/  oauth/  webui/  skill/
  sandbox/                   gate4ai run (srt / контейнер / microVM), doctor
  relayclient/               регистрация, туннель, DNS-провайдер ACME, отправка чекпоинтов свидетелю
  platform/                  ipc, paths, rpc, auth, version
docs/  e2e/
```

`pkg/` не заводится: внешний API ядра — бинарник, сокет и ABI; публичный Go-контракт — только `sdk-go`.

## Порядок перехода

Делить репозитории сразу нельзя: пока коннекторы — нативный Go внутри бинарника ядра, лицензии разделить невозможно. Поэтому сначала **границы внутри одного репозитория** (Go workspace, у каждого будущего репозитория свой `go.mod`), затем WASM, затем механическое разделение с сохранением истории.

1. **Лицензии и имя.** Ядро → FSL-1.1-ALv2, SPDX-заголовки, NOTICE; один бинарник `gate4ai` (`gate4ai daemon`); `go.work`.
2. **Relay отдельно.** Разрезать `internal/relay` на сервер (`relay/` со своим `go.mod`, без импортов ядра) и `internal/relayclient`; протокол — в `relay/docs/protocol.md`.
3. **SDK.** Вынести манифест и типы коннектора в `sdk-go/` (свой `go.mod`); ядро и коннекторы импортируют его; `DomainMatches` — туда же.
4. **WASM-хост** по connector-architecture.md («План перехода», шаги 1–2): capabilities в манифесте, wazero/Extism, функции хоста ABI v1, `capability_denied`, лимиты.
5. **Коннекторы на WASM** в `connectors/<имя>/` со своими `go.mod` (зависят только от `sdk-go`): files → mail (`tls_dial` с аутентификацией хостом) → browser (воркер уезжает в `internal/hostsvc/browser`). Убрать секреты из `Settings` и режим `gate4ai connector`.
6. **Магазин.** Формат `registry` (индекс, подписи cosign, отзыв), установка в ядре, CI-шаблон коннектора: воспроизводимая сборка `.wasm`, подпись, релиз.
7. **Разделение.** `git filter-repo --subdirectory-filter` для `relay/`, `sdk-go/`, `connectors/*` → отдельные репозитории с историей; в ядре остаются только `.wasm` официальных коннекторов как данные (или скачивание из registry).
8. **Публикация.** Проверить историю на секреты (`gitleaks`), CLA Assistant для ядра, DCO для коннекторов, branch protection, релизы GoReleaser + cosign, `.github` организации.

Шаги 1–3 и 7–8 механические; 4–6 — существенная работа, но она и так в целевой архитектуре.

## Расхождения с README (для правки человеком)

- `gate4aid` → `gate4ai daemon` (один бинарник).
- «Лицензия AGPL-3.0» → ядро FSL-1.1-ALv2, коннекторы AGPL-3.0 + исключение, SDK Apache-2.0.
- Relay: подчеркнуть, что он необязателен (нужен, если нет своего HTTPS до daemon).

## Решено

- Ядро (вместе с треем `gate4ai-tray`) и relay — FSL-1.1-ALv2 (2026-10-13).
- Переход выполнен (этапы 13–20): один бинарник `gate4ai`, relay и sdk-go — отдельные модули, коннекторы — WASM-модули под AGPL с исключением, магазин с подписанными пакетами и registry, `scripts/split.sh` делит монорепозиторий на репозитории с историей и проверяет сборку каждого. Отличия от этого документа и connector-architecture.md — в «Допущениях» PLAN.md (свой ABI на wazero без Extism, подписи Ed25519 вместо cosign, `internal/sandbox` вместо `isolation/`, сквозные тесты relay — в ядре).
- Организационный репозиторий `gate4ai/.github` живёт в монорепозитории как `github-org/` (каталог `.github/` занят CI самого ядра).
