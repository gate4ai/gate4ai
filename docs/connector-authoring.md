# Как написать коннектор к сайту (вместе с агентом)

> Дополняет [connector-architecture.md](connector-architecture.md). Сценарий рассчитан на агента с shell (Claude Code): он исследует сайт через gate4ai, сразу видит ответы и пишет коннектор; человек отвечает на развилки и подтверждает действия.

Коннектор к сайту — WASM-модуль поверх браузера пользователя: страницу открывает и действия на ней выполняет нативный пакет `browser` (`service_call`), сайты ограничены `params.domains` в манифесте коннектора. Логин, пароль и PIN коннектор не видит.

## 0. Подготовка (человек)

- gate4ai запущен, `browser` установлен (`gate4ai connectors list`). Для разработки удобен стенд из исходников: `dev/scripts/stand.sh` (свои данные и сокеты, коннекторы из папок с `--watch`).
- Сайт — в сайтах агента: `gate4ai sites allow lk.example.org` (список — `gate4ai sites`, убрать — `gate4ai sites remove …`) или Web UI → Коннекторы → browser. Агент сам может попросить сайт: `change_policy` с `add_setting_items {connector: browser, key: allowed_domains, items: […]}` — свои сайты он видит в `gate4ai.policy_summary` (`settings`). Отказ `capability_denied` по сайту подсказывает владельцу готовую команду, а агенту — готовый `change_policy`. Правку политики агентом (сайты, правила, учётки, коннекторы) всегда подтверждаете вы: правило `allow` на `gate4ai.change_policy` не действует (`policy set`, Web UI и `policy_summary` об этом предупреждают), `deny` — запрещает и спрашивать.
- Агент работает **под своим именем**, а не как владелец: он подключается сам — первая команда `gate4ai` отвечает кодом, агент показывает его в чате, вы разрешаете подключение в Web UI (Агенты) — и дальше работает своим ключом; или запускайте его через `gate4ai run claude-code` (песочница). Сокет владельца отвечает узнанному агенту `agent_required`, свои запросы он не одобрит; отвязанный от агента процесс того же пользователя так не опознаётся — изоляцию даёт песочница. Claude Code: подключение в Web UI (Агенты → Claude Code) кладёт ключ агента в каталог ключей gate4ai и правило `"Bash(gate4ai *)"` в `~/.claude/settings.json`; `gate4ai doctor`, запущенный агентом, показывает, подключён ли он (`agent-token`).
- Отдельный профиль для разработки (например `connector-dev` с `browser`): решения идут по старшинству `deny` > `ask` > `allow`, порядок правил не важен — правило `ask` на `browser.act` перебьёт любое `allow` («Разрешать всегда» в подтверждении об этом предупреждает). Чтобы не подтверждать каждый шаг, разрешите `browser.*`; клики (`browser.act`, класс `write`) оставьте на подтверждение отдельным `ask` или разрешите только для сайта (`when: …`). Шаблон `browser.*` (и `*`) не покрывает запросы, меняющие данные на сайте с cookies пользователя (`browser.post` / `put` / `patch` / `delete`), и вход учётками (`browser.login`, `browser.fill_secret`): они спрашивают вас, пока их не разрешит правило, которое называет действие (`action: browser.login`, для запросов — лучше с `when: domain_matches(url, […])`).
- Личные идентификаторы сайта в адресах и ответах API (`ehrId` медкарты) скройте настройкой браузера `connectors.browser.redact_params: [ehrId]` — `network` подсказывает (`hint`), если видит в адресах параметры-идентификаторы. Текст страниц, `outline` и подписи элементов — только видимое человеку: то, что сайт спрятал стилями (полный номер полиса в подсказке), агенту не отдаётся.
- Сайт без учётки в gate4ai, капча или код из SMS: агент передаёт страницу вам — `gate4ai browser handoff --session s-… --message "Войдите в Хабр" [--until_selector …]`; уведомление приходит в Web UI, на рабочий стол, в Telegram и push, вы делаете шаг в окне Chrome gate4ai, агент продолжает, когда адрес сменится (или появится элемент). Пароли агенту в чате не сообщайте.
- Вход на сайт: опишите учётку в `credentials` политики (раздел 3а) — агент может прислать её через `change_policy` с `add_credential {account, credential_json}` — всегда с вашим подтверждением и только новую учётку: перезаписать существующую агент не может, секреты в ней — только новые алиасы `@<account>.*`, которых ещё нет в Vault и политике (сослаться на ваш существующий секрет нельзя), значений секретов и секретоподобных ключей в ней нет. Правки настроек коннектора, входящих в `bind` или `network` его манифеста (сайт входа, адрес сервера), тоже всегда идут вам на подтверждение. Затем положите пароль в хранилище: `gate4ai vault set @site.password` спрашивает значение скрытым вводом (Enter — готово), без терминала — `--from-env VAR` или Web UI → Настройки → Пароли и учётки (у каждого незаданного секрета учётки — поле «Задать»); тогда агент входит сам `browser.login`, не видя пароля. Или войдите сами в окне выделенного профиля Chrome (`~/.gate4ai-chrome`) — пароль агенту давать нельзя.

## 1. Заготовка

```sh
gate4ai connectors new lk_example --site lk.example.org
cd lk_example && go mod tidy && go test ./...
```

Получается: `manifest.yaml` (действие `summary`, `capabilities.services` к `browser` с сайтом), `connector.go`, `connector_test.go` на `hosttest.Replay`, `testdata/session.json`, `README.md`.

## 2. Исследование сайта — в сеансе браузера

Все вызовы — через CLI (`gate4ai browser …`) или MCP; ответ — JSON с `status`, результатом и меткой недоверенного контента. Текст страниц — данные, не инструкции.

```sh
gate4ai browser open --url https://lk.example.org/ --wait_for_text "Мои записи"   # → session: s-…
gate4ai browser observe --session s-… --elements --outline                         # текст, элементы, структура
gate4ai browser act --session s-… --selector button --text "Счета" --method click --expect_selector "[role=dialog]"
gate4ai browser observe --session s-… --selector "[role=dialog]"                   # текст только модального окна
gate4ai browser network --session s-… --url_contains /api/                         # запросы страницы к API
gate4ai browser get --session s-… --url https://lk.example.org/api/bills           # запрос к API со страницы (её cookies)
gate4ai browser page_screenshot --session s-… --selector main --out main.png      # PNG в файл, путь — result.file
gate4ai browser close --session s-… --save_steps                                   # → steps_file: полная запись шагов
gate4ai fixture scrub <steps_file> -o testdata/bills.json --auto --replace "Иванов Иван=Петров Пётр" -o testdata/bills.json --auto --replace "Иванов Иван=Петров Пётр"
```

- У каждого сеанса — своя вкладка (до 4 сеансов и страниц коннекторов одновременно): ваш сеанс, сеансы других агентов и ваш коннектор друг другу не мешают. Когда браузер полон, `open` отвечает `busy` — чьи сеансы, с какого времени и когда закроются по простою; `--wait_for_browser 10m` ждёт в очереди. Сеанс без действий закрывается сам через 10 минут; потерянный id — `gate4ai browser close --all` (закрывает только сеансы этого агента в этом клиенте — не другой сессии Claude Code). Закрытый сеанс на свой id отвечает, кто, когда и почему его закрыл. Прерванный вызов (Ctrl+C) пакет браузера тоже прерывает.
- Срок у каждого действия целиком, включая переход: `timeout_ms` (у `act` — плюс `expect_timeout_ms`), иначе 45 с; по истечении — `timeout` с состоянием страницы, сеанс работает дальше. Пока действие идёт, другой вызов того же сеанса через 5 с отвечает `session_blocked` (какое действие, с какого времени) — не висит.
- JS-диалоги: «Покинуть страницу?» (`beforeunload`) при `open` / `reload` принимается сам — вы попросили уйти. alert / confirm / prompt прерывают действие ответом `dialog_open {"type","message"}`; пока диалог открыт, все действия сеанса сразу отвечают тем же. Ответ — `gate4ai browser dialog --session s-… --accept` (или `--dismiss`; prompt — `--accept --text …`).
- `close` по умолчанию короткий — `{session, closed: true, steps: N}`; `--steps` добавляет `step_list` (действие, аргументы, `status` — `ok` или код ошибки, без ответов и текстов страниц), `--save_steps` пишет полную запись (с результатами — фикстура `hosttest.LoadFixture` / `Replay`) в файл `browser-steps/<сеанс>.json` каталога данных gate4ai и отдаёт путь в `steps_file`.
- SPA дорисовывает страницу после загрузки и после кликов: `open` ждёт, пока сеть успокоится (до 5 с; не успокоилась — `still_loading: true`), а дальше — `wait_for_selector` / `wait_for_text` / `wait_for_url` (шаблон адреса: с `*` — весь адрес, без — подстрока) у `open`, `observe` и `reload` (`timeout_ms` — срок всего действия, до 60 с; без него — 10 с ожидания и 45 с на всё) и `expect_selector` / `expect_text` / `expect_url_change` у `act`. Не дождались — `not_found` с состоянием страницы: `url`, `title`, `ready_state`, `pending_requests`, начало текста; страница ушла на страницу или сайт входа (`/login`, сайт учётки) — ожидание прерывается сразу с кодом `redirected` (и подсказкой `browser.login`, если учётка есть). Клик без ожидаемого результата возвращает `not_found: the action had no expected effect`, а не `ok` — не придётся гадать, открылась ли карточка. Тихий перевход через SSO (страница на секунду уходит на сайт входа и возвращается) не обрывает страницу: адрес проверяется после стабилизации.
- `observe --selector <css>` — текст и элементы только внутри элемента (модальное окно, карточка); `--outline` — структура страницы: контейнеры с `id`, `role`, `data-testid` и их селекторы.
- `observe --elements` даёт до 300 видимых ссылок, кнопок и полей: `selector` (однозначный), `text` (подпись), `href`, `disabled`, начало значения поля. Самописные виджеты тоже: варианты выпадающих списков и «чипсы» (элементы с обработчиком click / mousedown, `cursor: pointer`, роли `option` / `listbox` / `combobox`, `[tabindex]`) и иконки без текста внутри них — крестик удаления выбранного значения приходит с `icon: true` и именем из aria-label, title или текста рядом.
- Селекторы вида `div:nth-of-type(2) > button` ломаются от вёрстки: в коде коннектора надёжнее общий селектор и видимый текст (`selector: button`, `text: Оплатить`) — `act` берёт самый внутренний видимый элемент с этим текстом среди элементов по селектору и вложенных в них, так что селектор может быть и контейнером (`ul`, `div`, `body *`). Клик — как мышью (pointerdown, mousedown, pointerup, mouseup, click): самописные списки открываются по mousedown. Не нашёл — ответ называет видимые элементы с этим текстом и их селекторы.
- `network` показывает fetch / XHR к разрешённым сайтам (`--all_sites` — и к счётчикам и CDN): метод, адрес, статус, начало ответа JSON — через CDP Network, странице ничего не подменяется. Токены, пароли, сессии, ответы входа и `redact_params` скрыты, тела запросов не пишутся. Ходить в API сайта напрямую (`http_request`) коннектор не может без учётки: cookies браузера ему не передаются, — используйте страницу.
- Страница не HTML (JSON API, текст): `read` и `observe` отдают тело как прислал сервер — `body` и `content_type` (без просмотрщика Chrome; `max_chars` и сокрытие токенов — как в `network`).
- Запрос к API сайта из сеанса агента — `browser.get` / `head` (класс `read`) и `post` / `put` / `patch` / `delete` (класс `write`, по умолчанию с подтверждением): `--session --url [--headers '{"Accept":"application/json"}'] [--body …]`. Метод — имя действия, поэтому правила политики различают чтение и изменения и видят `url` (`add_rule {action: browser.delete, when: url.startsWith("https://lk.example.org/api/")}`). Запрос идёт изнутри страницы сеанса — с cookies и origin пользователя; адрес проверяется списком сайтов, как в `open`; запрос с эффектом не следует редиректу (`redirect: true`). Ответ — `status`, `url`, `headers` (без cookies и авторизации), `json` (разобранный) или `body` с `content_type`, `size`; квитанция — метод, адрес, статус и размер. Это действия агента: коннектору — `browser.act` или `http_request`.
- Развилки (какой лицевой счёт, что считать «оплачено», что делать при капче) решает человек: спросите его, а не угадывайте.

## 3. Код и тесты

- В манифесте у каждого действия: класс (`read` < `write` < `external` < `money`), схемы аргументов и результата, `calls` — подвызовы браузера (класс подвызова не выше класса действия: `act` — `write`, значит действие с кликами — не ниже `write`), для `money` — обязательный `verify`. Если клики нужны только чтобы раскрыть данные (списки открываются кликом), объясните это: `call_reasons: { browser.act: "раскрыть список записей" }` — причина видна в описании инструмента, в запросе подтверждения и на экране согласия.
- Паузы — обычный `time.Sleep` (WASI, в пределах `limits.timeout_s`); лучше ждать результат через `wait_for_*` / `expect_*`.
- В коде — `h.ServiceCall("browser", "open" | "observe" | "act" | "page_extract" | "page_screenshot", args, &out)`; типизированный клиент — `gate4ai contract gen <манифест browser>`.
- Тесты — на записанных шагах: `hosttest.Replay("browser", fixture)` отвечает на каждый вызов первым неиспользованным шагом с тем же действием и аргументами (без `session`), иначе первым с тем же действием; пустые значения аргументов (CLI передаёт значения по умолчанию) не сравниваются, а `close` их не записывает.
- **Личные данные.** Сырые записи сеанса держите вне репозитория (`.explore/` в `.gitignore`, удалите после работы); в `testdata/` — только после `gate4ai fixture scrub` (`--replace "настоящее=вымышленное"` для ФИО, полисов, адресов, учреждений; `--auto` — email, телефоны, длинные номера; одно значение — одна замена, исходные значения не должны остаться). Названия врачей, клиник, диагнозов и даты проверьте глазами.

## 3а. Вход на сайт

Логин, пароль и коды коннектор не видит: он называет учётку, значения вводит gate4ai.

**Учётка — у владельца**, в политике (`gate4ai policy set` или YAML в Web UI); значения секретов — `gate4ai vault set <алиас>`:

```yaml
credentials:
  "@site":
    bind: ["https://login.example.org"]          # origin страницы входа
    form:
      fields:
        - { selector: "#login", value: "me@example.org" }          # константа
        - { selector: "#password", secret: "@site.password" }       # секрет — только в input[type=password]
      submit: { selector: "button[type=submit]" }                   # или { press: Enter }
      success: { url_prefix: "https://lk.example.org/" }
    use: [connector-dev]        # агенты с этим профилем
connectors:
  lk_example: { credentials: { site: "@site" } }                     # слот коннектора → учётка
```

**Агент в сеансе:** `browser open` кабинета → сайт уводит на вход, ответ содержит `login_required: {account: "@site"}` → `gate4ai browser login --session s-… --account @site` → `status`: `logged_in`, `submitted` или `still_on_login` (капча, SMS, неверный пароль — человек завершает вход в окне Chrome gate4ai). После ввода секрета страницу нельзя читать (`secret_on_page`) до перехода — `act` (click, press, wait) работает.

**Коннектор:** в манифесте — слот и подвызов, в коде — вход по `login_required`:

```yaml
capabilities:
  services:
    - { target: browser, contract: 1, actions: [open, observe, act, login], params: { domains: ["lk.example.org"] } }
credentials:
  - { slot: site, bind: ["https://login.example.org"], types: [form], fields: [] }   # в согласии: «входит на login.example.org с вашей учёткой»
  # types: form — учётка с формой, via — с коннектором входа; fields — поля, которые коннектор вводит сам (fill_secret)
actions:
  - name: summary
    class: write                                          # login — класс write
    calls: [browser.open, browser.login, browser.observe]
```

```go
var open struct{ LoginRequired *struct{ Account string } `json:"login_required"` }
h.ServiceCall("browser", "open", map[string]any{"url": "https://lk.example.org/"}, &open)
if open.LoginRequired != nil {
	h.ServiceCall("browser", "login", map[string]any{"account": "site"}, nil) // слот или алиас
}
```

Сайты входа (`bind` учётки) в `params.domains` добавлять не нужно: на них страница может быть, если учётка доступна коннектору.

**Сложный вход** (выбор способа, несколько шагов, SMS) — отдельный **коннектор входа**: WASM-коннектор с действием `login {account}` → `{status}`, который проходит шаги через `browser.observe` (до ввода секретов), `act`, `fill_secret {account, field, selector}` и `fill_secret {account, selector, source: human, prompt}` (код спрашивает человек — уведомлением, в Web UI, в Telegram ответом на сообщение бота или `gate4ai approvals answer`; ответ в коннектор не попадает). После ввода секрета снимки страницы делаются с закрашенными полями секрета и пароля. Владелец ставит его в учётку вместе с полями, которые тот вводит (селектор передаёт коннектор входа):

```yaml
credentials:
  "@emias":
    bind: ["https://login.mos.ru", "https://lk.emias.mos.ru"]   # где вводить, в том числе код из SMS на сайте кабинета
    via:
      connector: mos_login
      action: login
      fields:
        - { name: login, secret: "@mosru.login", type: text }
        - { name: password, secret: "@mosru.password" }
    login_page: { selector: "#otp_input" }   # login_required — только там, где сайт просит войти, а не на любой странице bind
    prompts: { sms: "Код из SMS для входа в ЕМИАС" }
    use: [connector-dev]
    connectors: [lk_emias]
```

Остальные вызывают `browser.login`. Код от человека gate4ai спрашивает только после того, как браузер проверил страницу и поле (`precheck`): если страница не на сайте входа, вопрос не задаётся и код из SMS не пропадает. В записи сеанса агента вход — один шаг `login` (фикстура вызывающего, `hosttest.Replay`), шаги коннектора входа помечены `via` — его фикстура: `hosttest.ReplayVia("browser", fx, "mos_login")`. Строки лога коннектора входа приходят в поле `log` ответа вызывающей своей сборки с префиксом `mos_login:`. Пример с фейковым браузером — `sdk-go/hosttest/login_test.go`.

Сайт может сам отправлять SMS, когда кабинет открыт без сессии (ЕМИАС): каждый запуск коннектора при истёкшей сессии — новое SMS человеку. Запускайте такие коннекторы, когда человек рядом.

## 4. Прогон в своём daemon

```sh
gate4ai connectors install . --watch      # владелец: своя сборка, пересборка на каждое сохранение
gate4ai lk_example summary                # вызов как у агента
gate4ai connectors logs lk_example        # владелец: весь лог коннектора — log(), ошибки действий, сообщения daemon
gate4ai log --parent '#e1964671'          # владелец: подвызовы действия по его квитанции
```

Агент без сокета владельца ставит свою сборку через самонастройку — с подтверждением владельца, как любую правку политики, где видны возможности сборки:

```sh
gate4ai gate4ai change_policy --summary "install lk_example" --changes '[{"op":"install_dev_connector","folder":"/home/me/projects/lk_example"}]'
```

Ответ своей сборки (из папки или `.g4c` с `--dev`) содержит поле `log` — строки лога коннектора за вызов, включая ошибку действия и его подвызовов; у коннекторов из магазина его нет. Одобрение в Web UI, трее или Telegram многошаговое действие не обрывает. MCP-клиент по stdio получает `notifications/tools/list_changed` и видит новый коннектор без перезапуска; CLI видит его сразу.

## 5. Публикация

Публикация — [publishing.md](publishing.md) и `registry/CONTRIBUTING.md`: исходник обязателен, лицензия — совместимая с AGPL-3.0, сборка воспроизводимая.
