# gate4ai sdk-go

SDK коннекторов [gate4.ai](https://github.com/gate4ai/gate4ai) на Go. Это контракт между хостом и коннектором: манифест, ABI и функции хоста.

SDK — каталог `sdk-go/` модуля `github.com/gate4ai/gate4ai` (одна версия с ядром): `go get github.com/gate4ai/gate4ai@latest`, импорт `github.com/gate4ai/gate4ai/sdk-go/...`. Пакеты SDK зависят только друг от друга и от `gopkg.in/yaml.v3` — это проверяет CI.

| Пакет | Что это |
|---|---|
| `manifest` | манифест: действия, аргументы, класс эффекта, проверка результата, `runtime`, `capabilities`, `limits`; шаблоны `${setting:…}` |
| `abi` | ABI: экспорты модуля, функции хоста, их запросы и ответы; `abi.Host` — типизированные функции хоста для кода коннектора |
| `connector` | PDK: `connector.Register` и экспорты WASM-модуля (`GOOS=wasip1`) |
| `native` | `runtime: native`: отдельный процесс под надзором хоста, JSON-RPC по stdio; `native.Serve` — тот же PDK, что у WASM |
| `contract` | контракт для вызовов между коннекторами (`service_call`): `Diff` / `Check` — несовместимые изменения, `Gen` — типизированный Go-клиент |
| `capability` | проверка вызовов по возможностям манифеста (`capability_denied`) |
| `hostfuncs` | реализация функций хоста поверх служб хоста; та же, что в хосте gate4.ai |
| `netauth` | TLS, STARTTLS и аутентификация хостом в IMAP/SMTP — коннектор не видит пароль |
| `hosttest` | хост в том же процессе для нативных тестов коннектора; `Replay` — фейк поставщика по шагам, записанным в сеансе браузера агента (`browser.close`), `LoadFixture` — их файл |
| `domain` | сопоставление адресов с шаблонами доменов, списки сайтов (белый / чёрный) |
| `registry` | индекс registry коннекторов (`index.json`): формат, проверка, последняя и отозванные версии; подпись индекса (Ed25519 над hex(sha256)) и ключи `ed25519:` — у хоста и у сайта gate4.ai одни правила |

## Принципы

- Коннектор пользуется только функциями хоста: сети, файлов, процессов и переменных окружения у WASM-модуля нет.
- Всё, что не объявлено в `capabilities` манифеста, недоступно: функция хоста вернёт `capability_denied`. Политика пользователя может возможности только сузить.
- Коннектор не видит секреты: в настройках — только алиасы (`@mail.work`), значения подставляет хост (`{{secret:@alias}}` в `http_request`, аутентификация в `tls_dial`) и только для адресов из `bind`.

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
	resp, err := h.HTTP(abi.HTTPRequest{URL: "https://api.example.com/hello",
		Headers: map[string]string{"Authorization": "Bearer {{secret:@example}}"}})
	if err != nil {
		return abi.ExecResult{}, err
	}
	return abi.ExecResult{Content: []abi.Content{abi.Text(string(resp.Body))}}, nil
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
  network: [ { host: api.example.com, scheme: https } ]
  secrets: [ { alias: "@example", bind: [api.example.com] } ]
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

Тесты — нативно, на `hosttest` (те же проверки возможностей, что в хосте). Примеры — коннекторы [`connectors/mail`](../connectors/mail) (WASM), [`connectors/browser`](../connectors/browser) (`runtime: native`, поставщик действий для других коннекторов).

## Вызовы других коннекторов

Поставщик перечисляет действия для других коннекторов в `services` (только для них — `service_only: true`) и номер контракта `contract`. Потребитель объявляет их в `capabilities.services`, а у каждого своего действия — его подвызовы `calls` (хост пропустит из действия только их и не выше его класса; разрешение действия их покрывает), и вызывает через `h.ServiceCall` или сгенерированный клиент:

```yaml
capabilities:
  services:
    - { target: browser, contract: 1, actions: [open, act], params: { domains: ["${setting:allowed_domains}"] } }
actions:
  - { name: pay, class: money, verify: feed, calls: [browser.open, browser.act] }
```

```sh
gate4ai contract gen contracts/browser.yaml -o browserclient/client.go   # клиент из копии манифеста поставщика
gate4ai contract diff old/manifest.yaml manifest.yaml                     # CI поставщика: ломающее изменение без нового contract — ошибка
```

`params` хост передаёт поставщику при каждом вызове (`ExecRequest.Caller`); вложенный вызов проходит политики хоста.

## runtime: native

Только для пакетов проверенного издателя (gate4.ai): процесс работает без песочницы, с правами пользователя; нативные пакеты сторонних издателей не принимаются.

```go
func main() { native.Serve(manifestYAML, &Browser{}) }
```

В манифесте — `runtime: native` и `platforms: [linux/amd64, darwin/arm64, …]`; `gate4ai connectors pack` собирает пакет под каждую платформу (`CGO_ENABLED=0 -trimpath`).

## ABI

Описание экспортов и импортов — в документации пакета [`abi`](abi/abi.go). Один экземпляр модуля обслуживает один вызов; лимиты памяти и времени — `limits` манифеста.

Формы данных — как у инструментов MCP, чтобы хост отдавал их агенту без перекодирования:

- аргументы действия — `input_schema`, подмножество JSON Schema (`object`, `string`, `number`, `integer`, `boolean`, `array`, вложенные объекты; `enum`, `format: email | url | path`, `default`, `minimum`/`maximum`, `maxLength`, `minItems`/`maxItems`); у объектов лишних полей не бывает;
- результат — `content` (блоки `text`, `image`, `resource`, `resource_link`) и/или `structured`; если в манифесте есть `output_schema`, `structured` обязателен и сверяется с ней;
- проверка — `verified` / `not_confirmed` / `verify_after`; ошибка `verify` — «проверить не удалось».

## Лицензия

[Apache-2.0](LICENSE) — только каталог `sdk-go/`; остальной репозиторий — AGPL-3.0. Коннекторы на этом SDK могут иметь любую совместимую лицензию; официальные коннекторы gate4.ai — AGPL-3.0.
