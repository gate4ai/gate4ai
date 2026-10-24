# gate4ai sdk-go

SDK коннекторов [gate4.ai](https://github.com/gate4ai/gate4ai) на Go. Это контракт между хостом и коннектором: манифест, ABI и функции хоста.

SDK — каталог `sdk-go/` модуля `github.com/gate4ai/gate4ai` (одна версия с ядром): `go get github.com/gate4ai/gate4ai@latest`, импорт `github.com/gate4ai/gate4ai/sdk-go/...`. Пакеты SDK зависят только друг от друга и от `gopkg.in/yaml.v3` — это проверяет CI.

| Пакет | Что это |
|---|---|
| `manifest` | манифест: действия, аргументы, класс эффекта, проверка результата, `runtime`, `capabilities`, `limits`; шаблоны `${setting:…}` |
| `abi` | ABI: экспорты модуля, функции хоста, их запросы и ответы; `abi.Host` — типизированные функции хоста для кода коннектора |
| `connector` | PDK: `connector.Register` и экспорты WASM-модуля (`GOOS=wasip1`) |
| `access` | списки доступа: домены и папки с режимом (`allow` / `read` / `append` / `connector` / `ask` / `deny`), сопоставление по самой конкретной строке — одни правила у хоста, коннекторов и Web UI |
| `contract` | контракт для вызовов между коннекторами (`service_call`): `Diff` / `Check` — несовместимые изменения, `Gen` — типизированный Go-клиент |
| `capability` | проверка вызовов по возможностям манифеста (`capability_denied`) |
| `hostfuncs` | реализация функций хоста поверх служб хоста; та же, что в хосте gate4.ai |
| `hosttest` | хост в том же процессе для нативных тестов коннектора; `Replay` — фейк поставщика по шагам, записанным в сеансе браузера агента (`browser.close`), `LoadFixture` — их файл |
| `domain` | сопоставление адресов с шаблонами доменов, списки сайтов (белый / чёрный) |
| `registry` | индекс registry коннекторов (`index.json`): формат, проверка, последняя и отозванные версии; подпись индекса (Ed25519 над hex(sha256)) и ключи `ed25519:` — у хоста и у сайта gate4.ai одни правила |

## Принципы

- Коннектор пользуется только функциями хоста: файлы в объявленных папках (`folders`), своё KV-хранилище (`capabilities.storage`), доказательства, лог и вызовы других коннекторов (`service_call`). Сети, процессов и переменных окружения у WASM-модуля нет; во внешний мир коннектор выходит только через `service_call` к браузеру gate4.ai — на сайты из `sites`.
- Всё, что не объявлено в манифесте, недоступно: функция хоста вернёт `capability_denied`. Политика пользователя может возможности только сузить.
- Сайты и папки объявляются с режимом доступа. `sites: [{domain, access}]` — `allow` (всё), `read` (только чтение, изменения — с подтверждением), `ask` (всё с подтверждением), `deny`; домен — `app.example.org`, `*.example.org` (включая сам `example.org`) или `*`. `folders: [{path, access}]` — `allow`, `append` (чтение, список и добавление), `read` (чтение и список), `deny`; путь — абсолютный или `~/…`, без масок — папка и всё внутри, `*`/`?` — внутри сегмента, `**` — любое число сегментов. Домен и путь могут быть шаблоном `${setting:…}`: список в настройке раскрывается в несколько строк с тем же режимом. Решает самая конкретная строка, при равенстве — строже; симлинки раскрываются, относительные пути запрещены. Проверка — `capability.Checker.Locate(path, op)` (`access.OpRead|OpList|OpAppend|OpWrite`), функции хоста `fs_*` выбирают операцию по имени.
- Коннектор не видит секреты: в формы сайтов их подставляет встроенный браузер хоста по учёткам входа владельца. Значения получает лишь компонент ядра (`runtime: builtin`) с `capabilities.reveal` — ключ модели браузера; всё раскрытое вырезается из доказательств и логов.

## Коннектор за пять минут

```go
package main

import (
	"context"
	_ "embed"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
)

//go:embed manifest.yaml
var manifestYAML []byte

type Hello struct{}

func (Hello) Execute(ctx context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	name, _ := r.Args["name"].(string)
	seen, _, err := h.KVGet("seen")
	if err != nil {
		return abi.ExecResult{}, err
	}
	if err := h.KVSet("seen", append(seen, name+"\n"...)); err != nil {
		return abi.ExecResult{}, err
	}
	return abi.ExecResult{Content: []abi.Content{abi.Text("hello, " + name)}}, nil
}

func (Hello) Verify(_ context.Context, _ *abi.Host, r abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
}

func init() { connector.Register(manifestYAML, Hello{}) }
func main() {}
```

```yaml
connector: hello
version: 1.0.0
runtime: wasm
capabilities:
  storage: { quota_mb: 1 }
actions:
  - name: say
    class: read
    input_schema:
      type: object
      properties:
        name: { type: string, description: who to greet }
      required: [name]
```

```sh
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o connector.wasm .
```

Тесты — нативно, на `hosttest` (те же проверки возможностей, что в хосте). Пример коннектора к сайту — [`e2e/testdata/pagereader`](../e2e/testdata/pagereader); поставщик действий — встроенный браузер gate4.ai ([`internal/connectors/builtin/browser`](../internal/connectors/builtin/browser)). Сайты коннектор открывает через него: см. «Вызовы других коннекторов».

## Вызовы других коннекторов

Поставщик перечисляет действия для других коннекторов в `services` (только для них — `service_only: true`) и номер контракта `contract`. Потребитель объявляет их в `capabilities.services`, а у каждого своего действия — его подвызовы `calls` (хост пропустит из действия только их и не выше его класса; разрешение действия их покрывает), и вызывает через `h.ServiceCall` или сгенерированный клиент:

```yaml
capabilities:
  services:
    - { target: browser, contract: 1, actions: [open, act] }
sites:
  - { domain: "${setting:domains}", access: allow }
folders:
  - { path: "~/Documents/bank", access: append }
actions:
  - { name: pay, class: money, verify: feed, calls: [browser.open, browser.act] }
```

```sh
gate4ai contract gen contracts/browser.yaml -o browserclient/client.go   # клиент из копии манифеста поставщика
gate4ai contract diff old/manifest.yaml manifest.yaml                     # CI поставщика: ломающее изменение без нового contract — ошибка
```

Сайты, на которых потребитель работает через браузер, он объявляет в `sites`; хост передаёт поставщику, кто его вызвал (`ExecRequest.Caller`), и вложенный вызов проходит политики хоста.

## runtime: builtin

Компоненты ядра gate4.ai — Браузер и Файлы — написаны на том же PDK (`connector.Connector`) и исполняются в процессе daemon на тех же функциях хоста с той же проверкой манифеста (`internal/connectors/wasm/builtin.go`). Из магазина они не ставятся; коннекторы магазина — только `runtime: wasm`.

## ABI

Описание экспортов и импортов — в документации пакета [`abi`](abi/abi.go). Один экземпляр модуля обслуживает один вызов; лимиты памяти и времени — `limits` манифеста.

Формы данных — как у инструментов MCP, чтобы хост отдавал их агенту без перекодирования:

- аргументы действия — `input_schema`, подмножество JSON Schema (`object`, `string`, `number`, `integer`, `boolean`, `array`, вложенные объекты; `enum`, `format: email | url | path`, `default`, `minimum`/`maximum`, `maxLength`, `minItems`/`maxItems`); у объектов лишних полей не бывает;
- результат — `content` (блоки `text`, `image`, `resource`, `resource_link`) и/или `structured`; если в манифесте есть `output_schema`, `structured` обязателен и сверяется с ней;
- проверка — `verified` / `not_confirmed` / `verify_after`; ошибка `verify` — «проверить не удалось».

## Лицензия

[Apache-2.0](LICENSE) — только каталог `sdk-go/`; остальной репозиторий — AGPL-3.0. Коннекторы на этом SDK могут иметь любую совместимую лицензию; официальные коннекторы gate4.ai — AGPL-3.0.
