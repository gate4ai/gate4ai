# CLI `gate4ai`: пользовательские пути и UX

Исследование от 2026-10-15: как владелец и агент проходят путь через `gate4ai`, `gate4ai-tray` и `install.sh`, где путь ломается и что исправить. Код не менялся. Поведение проверено на Linux на установленном `gate4ai 0.1.1` (`~/.local/bin`) и на сборке из исходников (`0.1.0-dev`); отличия macOS и Windows описаны по коду.

**Статус:** всё из раздела 9 сделано в этапе 24 [PLAN.md](../PLAN.md). Разделы 1–8 описывают поведение до исправлений (`0.1.1`), раздел 9 — что и как исправлено, раздел 10 — как теперь выглядит путь из запроса.

Источники: `cmd/gate4ai/*.go`, `internal/front/cli/*.go`, `internal/front/tray/tray.go`, `cmd/gate4ai-tray/main.go`, `internal/platform/paths/paths.go`, `internal/sandbox/doctor.go`, `www/install/install.sh`, `www/install/install.ps1`, [README](../README.md), [specification.md](specification.md).

## 1. Как устроено

- Один бинарник `gate4ai`. `gate4ai daemon` — долгоживущий процесс в foreground, без fork, автозапуска и сервисного режима. Все остальные команды, кроме офлайн-команд из раздела 2, — тонкие клиенты к daemon через сокет.
- Сокет владельца — `paths.OwnerSocket()`:
  - Linux: `$XDG_RUNTIME_DIR/gate4ai/gate4ai.sock` (обычно `/run/user/<uid>/gate4ai/gate4ai.sock`), без `XDG_RUNTIME_DIR` — `~/.local/state/gate4ai/run/gate4ai.sock`;
  - macOS: `~/Library/Application Support/gate4ai/run/gate4ai.sock`;
  - Windows: named pipe `\\.\pipe\gate4ai-<user>`.
  
  Если задан `GATE4AI_HOME`, сокет лежит в `$GATE4AI_HOME/run/`. Явный путь задают `GATE4AI_SOCKET` и `GATE4AI_AGENT_SOCKET`.
- Токен агента в `GATE4AI_TOKEN` переключает клиента на сокет агентов, а вместе с `GATE4AI_URL` — на HTTPS к публичному адресу устройства.
- Вывод — JSON на stdout (спецификация: «Машиночитаемый вывод: JSON по умолчанию; коды выхода…»). Ошибки печатает `Env.fail` в формате `{"status":"error","code":…,"message":…}`. Исключения: `tools`, `log` и `policy show` выводят текст, а usage уходит текстом в stderr.
- Коды выхода (`internal/front/cli/client.go`): `0` ok, `1` ошибка, `2` отказ политикой, `3` ждёт подтверждения, `4` проверка не прошла, `5` usage / 400 / 404, `6` daemon недоступен.
- `gate4ai-tray` раз в 5 секунд опрашивает `GET /v1/tray/state` через тот же клиент, что и CLI. Daemon трей не запускает. Если daemon недоступен, в меню остаётся неактивная строка «gate4.ai: daemon недоступен», а «Открыть Web UI» показывает уведомление с текстом ошибки Go.
- `install.sh` и `install.ps1` только кладут бинарники. По заявлению скрипта он «never starts the daemon», и в конце печатает:
  ```
  Next steps:
    gate4ai daemon        # start the guard (keep it running)
    gate4ai-tray &        # tray icon: status, Allow / Deny, Web UI
    gate4ai ui            # open the Web UI
  ```
  В выводе не сказано, что `gate4ai daemon` занимает терминал и что `gate4ai ui` нужно запускать в другом.

## 2. Перечень команд

Сверено с `gate4ai --help` (`usage` в `cli.go`) и с разбором аргументов в коде. Столбец «Daemon»: «да» — без daemon команда завершится с `unavailable`, exit 6.

| Команда | Кто | Daemon | Вывод | Замечания |
|---|---|---|---|---|
| `gate4ai`, `help`, `--help`, `-h` | все | нет | текст usage | |
| `version`, `--version` | все | нет | `gate4ai X.Y.Z` | |
| `daemon [флаги]` | владелец | — | slog в stderr | флаги: `--home --config --keystore --socket --agent-socket --connectors --ui --telegram-api --public --public-url --public-cert --public-key --egress --version`; в общем usage указаны только 4 из них |
| `tools [--json] [--as agent]` | агент / владелец | да | таблица или JSON | `--as` в usage не упомянут; при пустом каталоге ничего не печатает |
| `<connector> <action> [--arg v] [--wait] [--json] [--as]` | агент / владелец | да | JSON результата | алиас `mail` → `email`; `--help` на действии |
| `status <id> [--wait 10m]` | агент | да | JSON | |
| `skill` | агент | да | SKILL.md | |
| `mcp` | агент | да (при вызовах) | MCP на stdio | без `GATE4AI_TOKEN` — подсказка текстом, exit 5 |
| `log [--agent --action --effect --since --limit --json]` | владелец | да | таблица | `--json` в usage не упомянут; при пустом журнале ничего не печатает |
| `show <#hash\|id>` | владелец | да | JSON | |
| `export -o file [--passphrase-env VAR]` | владелец | да | JSON | |
| `verify file [--pubkey] [--passphrase-env]` | все | **нет** | JSON | офлайн |
| `journal verify` | владелец | да | JSON | |
| `agents [list] \| token <a> [--label] \| connect <a> \| revoke <a>` | владелец | да | JSON | usage при ошибке не упоминает `connect` |
| `policy [show] \| set <f> [-m] \| versions \| rollback <n> \| test …` | владелец | да | YAML / JSON | |
| `approvals [list] \| approve <id> \| reject <id>` | владелец | да | JSON | |
| `vault [list] \| set <alias> \| rm <alias>` | владелец | да | JSON | `vault rm` без аргумента молча возвращает exit 5 |
| `relay [status] \| register [--url --ca-file --self-signed]` | владелец | да | JSON | |
| `connectors [list] \| install \| update \| remove \| refresh` | владелец | да | JSON + диалог согласия | |
| `connectors pack \| verify \| keygen` | издатель | **нет** | JSON | офлайн |
| `registry check \| sign \| add` | registry | **нет** | JSON | офлайн (`check --fetch` ходит в сеть) |
| `ui [--print] [--remote]` | владелец | да | URL в stdout | открывает браузер через `xdg-open` / `open` / `rundll32` |
| `run <agent> [--workspace --container --microvm --image --engine --dry-run] [-- cmd]` | владелец | да | stderr + exit агента | `--image` и `--engine` в общем usage не упомянуты |
| `doctor` | все | частично | JSON проверок | проверка `daemon` → `fail`, exit 1 |
| `egress` | владелец | да | JSON | |

Неизвестное первое слово CLI считает именем коннектора и запрашивает каталог у daemon (`Env.action`).

## 3. Путь 1. «Установил → открыл интерфейс»

### Как сейчас

1. `curl -fsSL https://gate4.ai/install.sh | sh` → бинарники в `~/.local/bin`, затем «Next steps».
2. Пользователь сразу вводит `gate4ai ui`, как в запросе. Daemon не запущен, сокета нет. Воспроизведено на `0.1.1`:
   ```
   $ gate4ai ui
   {
     "code": "unavailable",
     "message": "gate4ai daemon is not running or not reachable: Post \"http://gate4ai/v1/ui/session\": dial unix /run/user/1000/gate4ai/gate4ai.sock: connect: no such file or directory",
     "status": "error"
   }
   $ echo $?
   6
   ```
3. Если человек всё же запустит `gate4ai daemon`, терминал будет занят и в нём пойдут строки slog:
   ```
   level=INFO msg="web UI" addr=http://127.0.0.1:7431 login="gate4ai ui"
   level=INFO msg="gate4ai daemon listening" owner=/run/user/1000/gate4ai/gate4ai.sock agent=…
   ```
   Если keychain недоступен, перед ними появляется `WARN`.
4. Во втором терминале `gate4ai ui` открывает браузер и печатает одноразовую ссылку `http://127.0.0.1:7431/login?t=…`, которая действует 2 минуты.
5. Закрыл терминал с daemon — daemon остановился. После перезагрузки всё нужно повторять.

### Проблемы

| # | Где | Что не так |
|---|---|---|
| U1 | шаг 2 | Человеку показан сырой JSON. В `message` смешаны понятная фраза и внутренности Go (`Post "http://gate4ai/…"`, `dial unix`). Не сказано, что делать дальше: ни `gate4ai daemon`, ни трея. |
| U2 | шаг 2 | Не различаются причины. «Сокета нет» значит, что daemon не запускали. `connection refused` на оставшемся сокете значит, что daemon упал (проверено: после `kill -9` сокет остаётся, и CLI выдаёт ту же ошибку). `invalid argument` значит, что путь сокета длиннее 108 байт: так бывает с длинным `GATE4AI_HOME`, и это проверено. На macOS предел 104 байта, а каталог `Application Support` длинный сам по себе. Третий случай особенно тёмный: daemon при этом тоже не стартует. |
| U3 | шаг 1 | В «Next steps» не сказано, что `gate4ai daemon` занимает терминал и что следующие команды выполняются в другом окне. README это объясняет, `install.sh` нет. |
| U4 | шаги 1, 5 | Нет автозапуска и фонового режима. Для продукта, описанного в спецификации как «приложение на компе (трей)», daemon в foreground — путь разработчика. PLAN «Нужно от человека» прямо указывает: «автозапуск при входе». |
| U5 | трей | `gate4ai-tray &` без daemon показывает только «daemon недоступен». Запустить daemon из меню нельзя. «Открыть Web UI» присылает уведомление с той же ошибкой Go. |
| U6 | `doctor` | Подсказка `start gate4ai daemon (or the tray app)` неверна: трей daemon не запускает. |
| U7 | шаг 3 | Если daemon уже запущен, повторный `gate4ai daemon` падает первым на порту Web UI (`listen tcp 127.0.0.1:7431: bind: address already in use`): `setup()` слушает UI раньше, чем `Serve` занимает сокет. Понятное `is in use by another daemon` видно, только если UI занят под другим адресом. |
| U8 | шаг 4 | На сервере без GUI (`ssh`) `xdg-open` не срабатывает. Ссылка напечатается, но она на loopback другой машины. Подсказки про `--remote` или проброс порта нет. |

### Предложение по исправлению сценария из запроса

**A. Понятная ошибка «daemon не запущен», для всех команд, которым нужен daemon (P0).**

В `Client.Do` при ошибке dial сохранять причину, а в `Env.fail` для `ErrUnavailable` формировать короткий `message`, отдельные `hint` и `detail`. Stdout остаётся JSON-ом, как требует спецификация, поля только добавляются, поэтому разбор агентами и скриптами не ломается:

```json
{
  "status": "error",
  "code": "unavailable",
  "message": "gate4ai daemon is not running",
  "hint": "start it: gate4ai daemon (keeps the terminal), or gate4ai service install (starts at login)",
  "detail": "no socket at /run/user/1000/gate4ai/gate4ai.sock"
}
```

Если stderr — терминал, туда же печатается та же подсказка одной-двумя строками для человека:

```
gate4ai: daemon is not running (no socket at /run/user/1000/gate4ai/gate4ai.sock).
Start it in another terminal: gate4ai daemon   — or run it at login: gate4ai service install
```

Тексты по причине:

| Причина (`errors.Is`) | message | hint |
|---|---|---|
| `ENOENT` / pipe не найден | daemon is not running | `gate4ai daemon` / `gate4ai service install` |
| `ECONNREFUSED` | daemon is not running (stale socket: it stopped or crashed) | то же + `see the daemon log` |
| `EINVAL` и длина пути > 104/108 | socket path is too long (N bytes, max 108) | задать `GATE4AI_SOCKET` короче или `GATE4AI_HOME` |
| `EACCES` | socket belongs to another user | запускать от того же пользователя |
| удалённый агент (`GATE4AI_URL`) | device is offline | агент повторяет позже (спецификация: «устройство офлайн») |

Для агента (`GATE4AI_TOKEN`) подсказка другая: «попросите владельца запустить gate4ai». Команды daemon агенту бесполезны.

**B. `gate4ai ui` запускает daemon сам (P0).**

Если daemon недоступен, а stdin и stdout — терминал, `gate4ai ui`:
1. выводит `gate4ai daemon is not running — starting it in the background…`;
2. запускает `gate4ai daemon` отдельным процессом (setsid / `DETACHED_PROCESS` на Windows), stdout/stderr пишутся в `<home>/daemon.log`;
3. до 5 секунд ждёт `GET /v1/health`, затем выдаёт ссылку входа и открывает браузер;
4. печатает, как остановить daemon и где лог, и советует `gate4ai service install`, чтобы daemon запускался при входе в систему.

Без терминала, в скриптах и с `--print` daemon сам не запускается: остаётся ошибка из пункта A, exit 6. Явное поведение — флаги `--start` и `--no-start`. Спецификации это не противоречит: daemon по-прежнему отдельный процесс `gate4ai daemon`, CLI его только запускает. Обещание «never starts the daemon» дано в `install.sh`, а не в спецификации, и установщик его не нарушит.

**C. Автозапуск: `gate4ai service install | uninstall | status` (P1).**

| ОС | Механизм |
|---|---|
| Linux | systemd user unit `~/.config/systemd/user/gate4ai.service` (`ExecStart=<path>/gate4ai daemon`, `Restart=on-failure`), `systemctl --user enable --now`. Без systemd — `~/.config/autostart/gate4ai.desktop`. Трей — `gate4ai-tray.desktop` в autostart |
| macOS | LaunchAgent `~/Library/LaunchAgents/ai.gate4.daemon.plist` (`RunAtLoad`, `KeepAlive`) и отдельный для трея |
| Windows | ключ `HKCU\…\Run` или задача Планировщика «при входе», без прав администратора |

`install.sh` и `install.ps1` в «Next steps» показывают `gate4ai service install` первым шагом, а daemon в foreground — для отладки. Нужен ли автозапуск, решает пользователь: с флагом `GATE4AI_START=1` установщик включит его сам, без флага только подскажет команду.

**D. Трей (P1).** Когда daemon недоступен, в меню появляется пункт «Запустить daemon» (тот же запуск, что в B) и строка с причиной из A. В уведомлении «Web UI не открыт» показываются `message` и `hint`, а не `err.Error()`. В `doctor` вместо `(or the tray app)` — подсказка из A.

## 4. Путь 2. Первая настройка: магазин → коннекторы → политика

Порядок по README: `policy set store.yaml` → `connectors install files …` → `policy set config.yaml` → `doctor`.

| # | Проблема |
|---|---|
| S1 | Пустое состояние почти не подсказывает, что делать. На свежем daemon `tools` и `log` ничего не печатают, `agents` возвращает `{"agents": null, "tokens": []}`, `connectors list` — `{"connectors": []}` (проверено). Подсказка есть только в комментарии `policy show`: `# gate4ai: no agents yet. Install connectors, then: gate4ai policy set config.yaml`. |
| S2 | Чтобы подключить магазин, нужен временный `store.yaml` из README с длинным ключом registry. Логично, чтобы официальный магазин был в пустой конфигурации по умолчанию (`emptyConfig`) или ставился командой `gate4ai connectors install files` без предварительной политики. Ключ registry по умолчанию уже указан в PLAN «Нужно от человека». |
| S3 | `connectors install` спрашивает согласие в stderr (`Install? [y/N]`). Без терминала, без `--yes` и без ответа на stdin пользователь не видит, что установка отменена. |
| S4 | Ошибки API дублируют код: `"message": "not_found: agent \"nobody\" is not in the configuration…"` при `"code": "not_found"` (проверено на `agents token nobody`). Причина: `fail` печатает `err.Error()`, а `APIError.Error()` = `code + ": " + message`. |

## 5. Путь 3. Подключение агента: токен → `tools` → действие → подтверждение

| # | Проблема |
|---|---|
| A1 | Неизвестная команда при неработающем daemon. `gate4ai foo` или опечатка вроде `gate4ai aprovals` приходит к `action`: она запрашивает `/v1/tools` и без daemon сообщает `unavailable`, а не «unknown command». Опечатку и неработающий daemon различить нельзя. С daemon ответ правильный: `unknown command or connector "foo"`. Предложение: без daemon выводить «unknown command "foo" (daemon is not running, so connector names could not be checked)» и предлагать ближайшую команду по расстоянию Левенштейна. |
| A2 | Неверный или отозванный токен даёт `unauthorized`, exit 1. В README этого кода выхода нет. Нужна подсказка: «получите новый токен: `gate4ai agents token <agent>` (у владельца)». |
| A3 | `gate4ai status` без id печатает usage, хотя человек ожидает статус daemon. Предложение: без аргумента — состояние daemon (как `/v1/health` плюс relay и число ожидающих подтверждений). `status <id>` работает как прежде. |
| A4 | `--help` у владельческих подкоманд (`ui --help`, `log --help`) печатает справку пакета `flag` со строкой `flag: help requested` и возвращает exit 5, будто это ошибка. При `flag.ErrHelp` нужно возвращать 0. У `tools --help` то же самое. |

## 6. Путь 4. Повседневная работа: подтверждения, журнал, Web UI

| # | Проблема |
|---|---|
| D1 | `approvals list` выдаёт только JSON. Человеку в терминале нужна таблица `id · агент · действие · сводка · почему`, а JSON — по флагу `--json`, как у `tools` и `log`. ⚠ **Расхождение со спецификацией**: «JSON по умолчанию». Безопасный вариант без правки спецификации — `--table` или единый `--human` для владельческих команд при JSON по умолчанию. Если сделать текст по умолчанию для TTY, нужно согласие на правку спецификации. |
| D2 | `ui` на сервере без GUI (U8): после `could not open a browser` подсказать `gate4ai ui --remote` (если настроен relay или `--public`) или `ssh -L 7431:127.0.0.1:7431`. |
| D3 | Ссылка живёт 2 минуты, а в выводе срок не указан. Нужно печатать `(valid for 2 minutes, one use)`. |
| D4 | Язык: CLI и `install.sh` на английском, трей и уведомления на русском, README на русском. Нужно выбрать язык интерфейса или взять его из локали (`LANG`). |

## 7. Путь 5. Сбои

| # | Проблема |
|---|---|
| F1 | Daemon упал (`kill -9`). Оставшийся сокет при следующем старте удаляется, это работает (проверено). Но CLI сообщает то же `unavailable`, без «it stopped or crashed», и лога нет: daemon пишет только в stderr своего терминала. Решается пунктами A и C (лог в файле). |
| F2 | CLI и daemon с разным окружением. Если daemon запущен с `XDG_RUNTIME_DIR`, а CLI без него (cron, `su`, часть ssh-сессий), они ищут сокет в разных местах (`/run/user/<uid>/…` и `~/.local/state/gate4ai/run/…`). `detail` с путём из пункта A сразу это покажет. Если сокета нет по основному пути, стоит заглянуть и по запасному. |
| F3 | Второй daemon — см. U7. Сначала занимать сокет владельца и только потом открывать UI и публичный адрес. Сообщение: `gate4ai daemon is already running (socket …); use gate4ai ui`. |

## 8. Отличия на macOS и Windows (по коду)

- **macOS:** сокет в `~/Library/Application Support/gate4ai/run/` — путь с пробелом и до предела в 104 байта остаётся мало места, поэтому случай `EINVAL` из пункта A на macOS вероятнее. Браузер открывается через `open`. Трей собирается только с cgo, иначе заглушка `built without cgo`. Автозапуск — launchd.
- **Windows:** named pipe `\\.\pipe\gate4ai-<user>`. Ошибка без daemon — «The system cannot find the file specified», её тоже надо относить к «not running». `install.ps1` печатает те же «Next steps». Браузер открывается через `rundll32 url.dll`. Трей собран с `-H windowsgui`. Автозапуск — `HKCU\…\Run` или Планировщик.
- **Linux:** без `XDG_RUNTIME_DIR` сокет лежит в `~/.local/state/gate4ai/run/` (см. F2). Уведомления трея идут через D-Bus.

## 9. Улучшения по приоритету и что сделано

| Приоритет | Что | Закрывает | Спецификация | Сделано |
|---|---|---|---|---|
| **P0** | Понятная ошибка `unavailable`: причина, `hint`, `detail`, строка для человека в stderr терминала; отдельный текст для агента и удалённого устройства | U1, U2, F1, F2 | совместимо | `internal/front/cli/errors.go` (`Explain`, `UnavailableError`) |
| **P0** | `gate4ai ui` в терминале запускает daemon в фоне и ждёт `/v1/health`; `--start` / `--no-start` | сценарий из запроса | совместимо | `cli.go` (`ui`), `daemonctl.go` |
| **P0** | «Next steps» в `install.sh` / `install.ps1`: `gate4ai service install`, `gate4ai ui`; `GATE4AI_START=1` | U3 | совместимо | `www/install/*`, `www/REQUIREMENTS.md` |
| P1 | `gate4ai service install/uninstall/status`; `gate4ai daemon start/stop/status`; журнал `<home>/daemon.log` | U4, F1 | совместимо | `service*.go`, `daemonctl.go`, `POST /v1/shutdown` |
| P1 | Трей: «Запустить daemon», понятные уведомления; подсказка `doctor` | U5, U6 | совместимо | `cmd/gate4ai-tray`, `internal/front/tray`, `internal/sandbox/doctor.go` |
| P1 | Повторный daemon: проверка сокета до Web UI, «already running»; длина пути сокета до запуска | U7, F3 | совместимо | `cmd/gate4ai/daemon.go`, `ipc.CheckPath`, `ipc.Alive` |
| P1 | Опечатки: ближайшая команда / коннектор / действие, в том числе без daemon | A1 | совместимо | `suggest` (Дамерау–Левенштейн) |
| P1 | Без дубля кода в `message` | S4 | совместимо | `Explain` |
| P2 | Пустые состояния с подсказками | S1 | совместимо | stderr |
| P2 | `--help` подкоманд — код 0; полные usage | A4 | совместимо | `commandHelp`, `usage`, `DaemonUsage` |
| P2 | `gate4ai status` без id — состояние daemon | A3 | совместимо | `daemonStatus` |
| P2 | `ui`: срок ссылки, подсказка без графики | D2, D3 | совместимо | `ui` |
| P2 | Официальный магазин в начальной конфигурации | S2 | совместимо | `emptyConfig`, README |
| P2 | Подсказка для `unauthorized`; код в README; отказ от согласия при установке коннектора | A2, S3 | совместимо | `Explain`, `consent` |
| P2 | Сокет по запасным путям (с `XDG_RUNTIME_DIR` и без) | F2 | совместимо | `paths.FindOwnerSocket` |
| P3 | Таблицы для человека: списки и состояние владельца, ошибки текстом | D1 | спецификация обновлена с согласия человека (2026-10-15) | если stdout — терминал (`IsTerminal`: ioctl TCGETS / TIOCGETA, GetConsoleMode); `--json`, `GATE4AI_OUTPUT=json\|table`; `output.go` |
| P3 | Язык трея | D4 | вне спецификации | русский при русской локали, иначе английский (как CLI); `GATE4AI_LANG` |

Не сделано: вопрос «включить автозапуск? (Y/n)» в установщике (в `curl | sh` stdin занят скриптом; вместо него `GATE4AI_START=1`), трей сразу после `service install` не запускается (может быть уже запущен руками). Вживую на macOS и Windows автозапуск не проверялся (PLAN, «Нужно от человека»).

## 10. Путь из запроса теперь

Скрипт или агент (stdout — pipe или файл) — та же ошибка с кодом 6, но с причиной и подсказкой:

```
$ gate4ai ui --no-start | cat
{
  "code": "unavailable",
  "detail": "no socket at /run/user/1000/gate4ai/gate4ai.sock",
  "hint": "start it: gate4ai daemon start (in the background) or gate4ai service install (also at every login); gate4ai daemon runs it in this terminal",
  "message": "gate4ai daemon is not running",
  "status": "error"
}
```

Человек в терминале с `--no-start` — только текст:

```
$ gate4ai ui --no-start
gate4ai daemon is not running.
Start it: gate4ai daemon start (in the background) or gate4ai service install (also at every login); gate4ai daemon runs it in this terminal.
(no socket at /run/user/1000/gate4ai/gate4ai.sock)
```

Человек в терминале без флагов — daemon поднимается сам:

```
$ gate4ai ui
Starting gate4ai daemon in the background (log: ~/.local/state/gate4ai/daemon.log)…
gate4ai daemon is running. Stop it: gate4ai daemon stop. Start it at every login: gate4ai service install
One-time login link, valid for 2 minutes:
http://127.0.0.1:7431/login?t=…
```

Упавший daemon (`connection refused` на старом сокете) — «it stopped or crashed, the socket is left over» и путь к журналу; слишком длинный путь сокета — «the daemon socket path is too long» с длиной и пределом; повторный `gate4ai daemon` — `already running (socket …)`.

### Как CLI отличает человека от скрипта

По тому, куда подключены потоки (`IsTerminal`: `ioctl TCGETS` на Linux, `TIOCGETA` на macOS, `GetConsoleMode` на Windows; `/dev/null` терминалом не считается):

- **формат вывода** — по stdout: терминал — таблицы и текстовые ошибки; pipe или файл (`| jq`, `$(…)`, агенты, cron, CI) — JSON;
- **подсказки** в stderr — только если stderr — терминал;
- **запуск daemon из `gate4ai ui`** — только если stdin и stderr — терминал и нет `--print`.

Псевдотерминал (`ssh -t`, `script`, `expect`, CI с pty) выглядит как человек — для него `--json` или `GATE4AI_OUTPUT=json` и `gate4ai ui --no-start`.
