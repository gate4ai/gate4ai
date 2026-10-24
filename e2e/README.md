# gate4ai e2e

Сквозные тесты [gate4ai](https://github.com/gate4ai/gate4ai): хост собирается из исходников (Браузер и Файлы — внутри него), тестовый коннектор `testdata/pagereader` ставится как своя сборка, `daemon` и CLI работают по-настоящему. Фейковые здесь только внешние серверы: сайты (`httptest`), Telegram Bot API.

| Тест | Что проверяет |
|---|---|
| `TestQuickstartFlow` | конфигурация из `docs/quickstart.md` принимается; `files.append` в папку из списка доступа проходит с проверкой, чтение вне списка запрещено |
| `TestRelayDNSForACME` | DNS собранного `gate4ai-relay` годится для ACME DNS-01: certmagic находит зону relay и её NS (иначе сертификат устройства не выпускается, и ссылки на `<id>.relay.gate4.ai` висят до таймаута) |
| `TestRemoteLinkThroughRelay` | daemon подключается к локальному relay; ссылка «Вход с телефона» из Настроек открывает Web UI устройства через relay (TLS до устройства), второй раз не открывается (срок ссылки проверяет unit-тест ядра `TestLoginLinkExpires` со сдвигом часов) |
| `TestConnectorFromSourceFolder` | коннектор ставится из папки с исходниками (копия `testdata/pagereader`), `update` пересобирает из неё, `--watch` переставляет после правки манифеста |
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
