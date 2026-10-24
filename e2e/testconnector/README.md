# testconnector — коннектор gate4.ai к тестовому сайту

WASM-коннектор к [тестовому сайту gate4ai](../testsite) поверх браузера пользователя: страницы открывает и действия выполняет встроенный Браузер (`service_call`), своей сети у коннектора нет, значений учётки он не видит. Фикстура сквозных тестов и образец для своих коннекторов ([connector-authoring.md](../../docs/connector-authoring.md)).

| Действие | Класс | Что делает |
|---|---|---|
| `read` | read | таблица показаний с главной (`rows: [{meter, value}]`) |
| `cabinet` | write | открывает кабинет; если сайт увёл на вход — `browser.login` учёткой слота `site` |
| `press` | write, `verify` | нажимает кнопку в кабинете, возвращает номер заявки; `verify` сверяет его с текстом на странице |

Адрес сайта — аргумент `--url` или настройка `connectors.testconnector.site`.

## Прогон в своём daemon

```sh
go run ./e2e/testsite/cmd/testsite &                        # http://127.0.0.1:8099/, учётка user-e2e / fake-pass-e2e-4242
gate4ai connectors install e2e/testconnector --yes --watch   # своя сборка, пересборка на каждое сохранение
gate4ai policy set policy.yaml -m "testconnector"            # политика ниже
gate4ai vault set @site.password                              # пароль скрытым вводом (без терминала — --from-env VAR)
gate4ai testconnector read
gate4ai testconnector cabinet
gate4ai testconnector press                                  # class write: ждёт подтверждения; в ответе verified: true
```

`policy.yaml` (фрагмент; `dev/scripts/stand.sh` поднимает сайт на том же порту):

```yaml
profiles:
  assistant:
    connectors: [testconnector]
access:
  sites: [{ domain: "127.0.0.1", access: allow }]
connectors:
  testconnector:
    domains: ["127.0.0.1"]
    site: http://127.0.0.1:8099
    login_site: http://127.0.0.1:8099
    credentials: { site: "@site" }
credentials:
  "@site":
    bind: ["http://127.0.0.1:8099"]
    login_page: { selector: "#login" }        # вход нужен только там, где есть форма
    form:
      fields:
        - { selector: "#login", value: user-e2e }
        - { selector: "#password", secret: "@site.password" }
      submit: { selector: "#bind" }
      success: { url_prefix: "http://127.0.0.1:8099/cabinet" }
    use: [assistant]
```

## Тесты

`go test ./e2e/testconnector` — на записанных шагах браузера (`testdata/session.json`, `hosttest.Replay`). Сквозные — `./scripts/ci.sh e2e` (`TestServiceCallToNativeBrowser`, `TestBrowserLoginByAccount`, `TestConnectorFromSourceFolder`).
