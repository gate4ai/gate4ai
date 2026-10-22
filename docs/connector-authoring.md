# Как написать коннектор к сайту (вместе с агентом)

> Дополняет [connector-architecture.md](connector-architecture.md). Сценарий рассчитан на агента с shell (Claude Code): он исследует сайт через gate4ai, сразу видит ответы и пишет коннектор; человек отвечает на развилки и подтверждает действия.

Коннектор к сайту — WASM-модуль поверх браузера пользователя: страницу открывает и действия на ней выполняет нативный пакет `browser` (`service_call`), сайты ограничены `params.domains` в манифесте коннектора. Логин, пароль и PIN коннектор не видит.

## 0. Подготовка (человек)

- gate4ai запущен, `browser` установлен (`gate4ai connectors list`). Для разработки удобен стенд из исходников: `dev/scripts/stand.sh` (свои данные и сокеты, коннекторы из папок с `--watch`).
- Сайт — в сайтах агента: `gate4ai sites allow lk.example.org` (список — `gate4ai sites`, убрать — `gate4ai sites remove …`) или Web UI → Коннекторы → browser. Агент сам может попросить сайт: `change_policy` с `add_setting_items {connector: browser, key: allowed_domains, items: […]}` — свои сайты он видит в `gate4ai.policy_summary` (`settings`). Отказ `capability_denied` по сайту подсказывает владельцу готовую команду, а агенту — готовый `change_policy`. Правка политики агентом идёт по обычным правилам: без правила — с вашего подтверждения; правило `allow` на `gate4ai.change_policy` («Разрешать» у группы «Изменение» коннектора gate4ai) позволит агенту расширять свои права без вопроса — Web UI об этом предупреждает.
- Агент работает **под своим именем**, а не как владелец: дайте ему токен (`gate4ai agents token claude-code` — показывается один раз — и `export GATE4AI_TOKEN=g4a_…` в окружении, где запускается агент) или запускайте его через `gate4ai run claude-code` (песочница). Без токена CLI на этом компьютере ходит через сокет владельца: агенту тогда доступны команды владельца, вплоть до `gate4ai approvals approve` своих же запросов, а `--as <агент>` — лишь добровольная условность. Claude Code: подключение в Web UI (Агенты → Claude Code) само кладёт токен в `env` файла `~/.claude/settings.json` вместе с правилом `"Bash(gate4ai *)"`; `gate4ai doctor`, запущенный агентом, предупреждает о правах владельца (`agent-token`).
- Отдельный профиль для разработки (например `connector-dev` с `browser`): решения идут по старшинству `deny` > `ask` > `allow`, порядок правил не важен — правило `ask` на `browser.act` перебьёт любое `allow` («Разрешать всегда» в подтверждении об этом предупреждает). Чтобы не подтверждать каждый шаг, разрешите `browser.*`; клики (`browser.act`, класс `write`) оставьте на подтверждение отдельным `ask` или разрешите только для сайта (`when: …`).
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
gate4ai browser close --session s-… > .explore/bills.json                          # шаги сеанса
gate4ai fixture scrub .explore/bills.json -o testdata/bills.json --auto --replace "Иванов Иван=Петров Пётр"
```

- Страница одна на весь браузер: пока сеанс открыт, другие вызовы браузера (и ваш коннектор) получают `busy` с подсказкой. Закрывайте сеанс перед запуском коннектора; сеанс без действий закрывается сам через 10 минут, потерянный id не мешает — `gate4ai browser close --all`. Прерванный вызов (Ctrl+C) пакет браузера тоже прерывает и освобождает страницу.
- SPA дорисовывает страницу после загрузки и после кликов: `open` ждёт, пока сеть успокоится (до 5 с), а дальше — `wait_for_selector` / `wait_for_text` у `open` и `observe` и `expect_selector` / `expect_text` / `expect_url_change` у `act`. Клик без ожидаемого результата возвращает `not_found: the action had no expected effect`, а не `ok` — не придётся гадать, открылась ли карточка. Тихий перевход через SSO (страница на секунду уходит на сайт входа и возвращается) не обрывает страницу: адрес проверяется после стабилизации.
- `observe --selector <css>` — текст и элементы только внутри элемента (модальное окно, карточка); `--outline` — структура страницы: контейнеры с `id`, `role`, `data-testid` и их селекторы.
- `observe --elements` даёт до 300 видимых ссылок, кнопок и полей: `selector` (однозначный), `text` (подпись), `href`, `disabled`. Значения полей не возвращаются.
- Селекторы вида `div:nth-of-type(2) > button` ломаются от вёрстки: в коде коннектора надёжнее общий селектор и видимый текст (`selector: button`, `text: Оплатить`) — `act` берёт первый видимый элемент с этим текстом.
- `network` показывает fetch / XHR к разрешённым сайтам (`--all_sites` — и к счётчикам и CDN): метод, адрес, статус, начало ответа JSON — через CDP Network, странице ничего не подменяется. Токены, пароли, сессии, ответы входа и `redact_params` скрыты, тела запросов не пишутся. Ходить в API сайта напрямую (`http_request`) коннектор не может без учётки: cookies браузера ему не передаются, — используйте страницу.
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

Агент без сокета владельца ставит свою сборку через самонастройку — всегда с подтверждением владельца (правило `allow` на `change_policy` его не отменяет), где видны возможности сборки:

```sh
gate4ai gate4ai change_policy --summary "install lk_example" --changes '[{"op":"install_dev_connector","folder":"/home/me/projects/lk_example"}]'
```

Ответ своей сборки (из папки или `.g4c` с `--dev`) содержит поле `log` — строки лога коннектора за вызов, включая ошибку действия и его подвызовов; у коннекторов из магазина его нет. Одобрение в Web UI, трее или Telegram многошаговое действие не обрывает. MCP-клиент по stdio получает `notifications/tools/list_changed` и видит новый коннектор без перезапуска; CLI видит его сразу.

## 5. Публикация

Публикация — [publishing.md](publishing.md) и `registry/CONTRIBUTING.md`: исходник обязателен, лицензия — совместимая с AGPL-3.0, сборка воспроизводимая.
