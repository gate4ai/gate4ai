# gate4ai e2e

Сквозные тесты [gate4ai](https://github.com/gate4ai/gate4ai): хост собирается из исходников (Браузер и Файлы — внутри него), тестовый коннектор `testconnector` ставится как своя сборка, `daemon` и CLI работают по-настоящему. Фейковые здесь только внешние серверы: тестовый сайт `testsite` (`httptest`), Telegram Bot API.

## Тестовый сайт и коннектор

- **`testsite`** — пакет с `http.Handler` тестового сайта gate4ai (состояние в памяти): `/` — таблица показаний, `/login` — вход фиксированной учёткой (`user-e2e` / `fake-pass-e2e-4242`, напечатана на странице; это не секрет), `/cabinet` — кабинет с кнопкой `POST /cabinet/action` и видимым результатом «Заявка №N принята», `/reset` — сброс сессий; страницы тестов Браузера — `/tags`, `/publish` и `/other`, `/live`, `POST /api/items`. Руками: `go run ./e2e/testsite/cmd/testsite` (`http://127.0.0.1:8099/`); `dev/scripts/stand.sh` поднимает его рядом с daemon стенда. Персональных данных нет, `noindex`.
- **`testconnector`** — WASM-коннектор к сайту поверх Браузера (`service_call`): `read` — строки таблицы, `cabinet` — вход по слоту `site` через `browser.login`, `press` — класс `write`: клик по кнопке, `verify` сверяет номер заявки с текстом на странице. Юнит-тесты — `hosttest.Replay` по `testdata/session.json` (`go test ./e2e/testconnector`); установка в свой daemon — `gate4ai connectors install e2e/testconnector --yes`; политика и учётка — [testconnector/README.md](testconnector/README.md).

| Тест | Что проверяет |
|---|---|
| `TestQuickstartFlow` | конфигурация из `docs/quickstart.md` принимается; `files.append` в папку из списка доступа проходит с проверкой, чтение вне списка запрещено |
| `TestRelayDNSForACME` | DNS собранного `gate4ai-relay` годится для ACME DNS-01: certmagic находит зону relay и её NS (иначе сертификат устройства не выпускается, и ссылки на `<id>.relay.gate4.ai` висят до таймаута) |
| `TestRemoteLinkThroughRelay` | daemon подключается к локальному relay; адрес «Вход с телефона» из Настроек открывает через relay (TLS до устройства) страницу входа Web UI устройства, пароль даёт сессию |
| `TestConnectorFromSourceFolder` | коннектор ставится из папки с исходниками (копия `testconnector`), `update` пересобирает из неё, `--watch` переставляет после правки манифеста |
| `TestServiceCallToNativeBrowser` | `testconnector.read` через `service_call` к Браузеру в headless Chrome читает таблицу `testsite`; сайт вне `sites` коннектора отклоняется; действия Браузера только для коннекторов агенту не видны |
| `TestBrowserLoginByAccount` | вход учёткой из Vault: агентом (`browser.open` → `login_required` → `browser.login`) и из `testconnector.cabinet`; `testconnector.press` ждёт подтверждения владельца, после него исполняется и `verified` по странице; пароль не попадает в ответы и журнал |
| `TestBrowserAgentProfile` | сеансы двух агентов на `testsite` (`/publish` с `beforeunload`, `/other`): `close --all` закрывает только свои; `browser.post` на сайте `read` ждёт владельца |
| `TestBrowserLiveView` | живой просмотр сеанса (`/live`): клики и ввод из Web UI доходят до страницы |
| `TestBrowserTypeAndScreenshot` | агент через CLI в headless Chrome: `act --method type --value "a, b, c" --submit Enter` создаёт три тега в поле, реагирующем на keydown (ответ — `changes.added`), `press --value ","` — ещё один; `page_screenshot` с `--out`, окно, вся страница, элемент — валидные PNG; скрытый элемент объяснён (`display:none`); клик без ожидаемого результата — `not_found` с путём к снимку; в доказательствах журнала поле пароля закрашено |
| `TestRemoteLoginQR` | в headless Chrome (chromedp): «Настройки → Показать ссылку и QR-код» — картинка загрузилась, QR расшифровывается (gozxing) в ту же ссылку, что на странице |

## Запуск

Хост, relay и коннекторы собираются из этого же репозитория (`GOWORK=off`), а не из опубликованных версий. Бинарники `gate4ai` и `gate4ai-relay` собираются один раз на прогон (`TestMain`, общий каталог). Старт daemon и relay ждётся по готовности до 60 с (`StartTimeout`); если процесс завершился раньше — ошибка сразу, а relay, у которого заняли порт, запускается заново на других портах.

```sh
./scripts/ci.sh e2e    # из корня репозитория; меньше минуты: тесты идут параллельно (t.Parallel); -short пропускает всё; без Chrome тесты браузера пропускаются
```

## CI

Задача `e2e` в [ci.yml](../.github/workflows/ci.yml): каждый pull request, каждый коммит в `main` и каждый релиз проверяются целиком.

Направление зависимостей: e2e импортирует что угодно, его — никто (`scripts/deps-check.sh`). Хост не импортирует коннекторы даже в тестах.

## Лицензия

[AGPL-3.0-only](../LICENSE). Вклад — по CLA и DCO.
