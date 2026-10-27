# gate4ai на сервере без графики (рядом с OpenClaw)

> Дополняет «Режимы развёртывания» (постоянно включённое устройство), «Браузер» и «Изоляция локальных агентов» в [спецификации](specification.md). Как устроен просмотр сеансов и почему так, — [browser-live-view.md](browser-live-view.md).

## Задача

На VPS или домашнем сервере без X работает OpenClaw. Рядом стоит gate4ai: агент сам работает в браузере gate4ai под политиками, а когда нужен человек (капча, код из SMS, вход без учётки в gate4ai, «данные подготовлены — нажмите сами»), зовёт его ссылкой. По ссылке человек с телефона или ноутбука видит страницу и управляет ею, затем агент продолжает.

## Как это работает

```
 телефон / ноутбук владельца                          сервер (без X)
 ┌───────────────────────┐   HTTPS   ┌──────────┐   ┌───────────────────────────────────────────────┐
 │ чат OpenClaw / Telegram│──────────▶│  relay   │──▶│ пользователь ОС gate4ai                        │
 │ «смотрите: <адрес>/ui/ │  /ui/…    │(passthru)│   │  gate4ai daemon (systemd --user, linger)       │
 │  browser/s-…»          │◀──────────│          │   │   ├ /ui/browser/<сеанс> — вход владельца,      │
 │ просмотр в браузере    │ websocket └──────────┘   │   │  кадры и ввод по websocket                  │
 └───────────────────────┘                          │   └ браузер (в процессе daemon)                 │
                                                     │       ├ Xvfb :N (без TCP, cookie доступа)      │
                                                     │       └ Chrome: CDP по pipe, порта нет;        │
                                                     │         сеанс агента — своё окно               │
                                                     ├───────────────────────────────────────────────┤
                                                     │ пользователь ОС openclaw                       │
                                                     │  OpenClaw → exec → `gate4ai …` CLI (ключ       │
                                                     │  агента; к Chrome, Xvfb, данным gate4ai        │
                                                     │  доступа нет)                                  │
                                                     └───────────────────────────────────────────────┘
```

1. Агент: `gate4ai browser open --url …` — ответ несёт `session` и `view_url` (`https://<устройство>/ui/browser/s-…`). Агент может сразу прислать ссылку в свой чат.
2. Человек открывает ссылку. Без входа в Web UI — страница входа, после входа (пароль и/или Telegram) — сразу этот сеанс: страница вживую, что делает агент.
3. Капча, SMS, вход без учётки: агент вызывает `gate4ai browser handoff --session s-… --message "Введите код из SMS"`. Уведомление в Telegram-боте gate4ai, push и Web UI несёт ту же ссылку; управление включено сразу, «Готово» / «Не буду» завершают ожидание.
4. Взять сеанс можно и без просьбы агента — «Взять управление». С другого устройства это подтверждается в Telegram, если вход был без него. Пока сеанс у человека, действия агента в нём отвечают `session_held_by_user`.

Ссылка сама доступа не даёт: агент её знает, но войти в Web UI не может — вход только по паролю. Пароль есть с первой секунды: daemon генерирует его при первом запуске, установщик и `gate4ai ui --print` печатают его (до первого входа) вместе с адресом из интернета `https://<id>.relay.gate4.ai/ui/` — входите по нему с телефона или ноутбука, проброс порта по ssh не нужен. Если gate4ai ставил агент, он пересказывает эти строки вам в чат (`gate4ai ui --print` — единственная команда владельца, которая отвечает и агенту; после перезапуска daemon до первого входа она печатает новый пароль): смените пароль после первого входа (Настройки → Вход) и включите там подтверждение в Telegram — тогда знание пароля агентом ничего не даёт.

## Установка

Установщик рассчитан и на агента без терминала: ничего не спрашивает, в конце печатает в stdout строки `Web UI:`, `Password:` и `From the internet:` (ждёт адрес из интернета до `GATE4AI_UI_WAIT`, по умолчанию 3 минуты), а недостающее для Chrome ставит сам, если он root или `sudo -n` проходит, иначе печатает точные команды для root — «To run Chrome, install: libnss3 libgbm1 …». Поставить можно под тем же пользователем, что и OpenClaw (проще), или под отдельным (строже — см. «Изоляция»).

```sh
# один пользователь (openclaw): gate4ai и агент рядом; у пользователя должен быть linger,
# иначе systemd --user и служба умирают с выходом из сессии
loginctl enable-linger "$USER"                            # root: loginctl enable-linger openclaw
curl -fsSL https://gate4.ai/install.sh | GATE4AI_NO_TRAY=1 sh
#   «The browser of agents»: Chrome (системный, snap, flatpak, кеш Playwright/Puppeteer), его библиотеки (ldd),
#   Xvfb и шрифты; в терминале — «Install now? [y/N]», без терминала — сам при root / sudo -n,
#   иначе команды для root; GATE4AI_INSTALL_DEPS=1 — поставить без вопроса
#   в конце: Web UI: http://127.0.0.1:7431/   Password: xxxxx-xxxxx-xxxxx-xxxxx   From the internet: https://<id>.relay.gate4.ai/ui/
gate4ai ui --print                                        # те же строки в любой момент (--json — для программы)
gate4ai skill > ~/.openclaw/skills/gate4ai/SKILL.md       # агент подключается сам: первая команда даёт код, подтвердите его в Web UI или Telegram
# в настройках OpenClaw выключите его собственный браузер для сайтов с личными данными (или целиком)
```

```sh
# два пользователя: отдельные пользователи ОС для gate4ai и для агента (root)
useradd -m gate4ai && useradd -m openclaw
loginctl enable-linger gate4ai

# пользователь gate4ai (sudo — только для Chrome, его библиотек и Xvfb: установщик спросит)
curl -fsSL https://gate4.ai/install.sh | GATE4AI_NO_TRAY=1 sh
gate4ai service install --no-tray -- --keystore file   # сервер без keychain
gate4ai ui --print                                        # адреса и пароль; From the internet — адрес для телефона и агентов
gate4ai relay status                                      # состояние адреса из интернета: daemon регистрируется на relay.gate4.ai сам

# пользователь openclaw: нужен только CLI
curl -fsSL https://gate4.ai/install.sh | GATE4AI_NO_START=1 GATE4AI_NO_OPEN=1 GATE4AI_NO_BROWSER=1 GATE4AI_NO_TRAY=1 sh
export GATE4AI_URL=https://<device-id>.relay.gate4.ai
gate4ai pair --wait 5m                                    # код подтвердить в Web UI или Telegram
gate4ai skill > ~/.openclaw/skills/gate4ai/SKILL.md
```

Chrome без root: бинарник ставится без root — Chromium из кеша Playwright или Puppeteer gate4ai находит сам (свой путь — `GATE4AI_CHROME`), а `gate4ai browser setup --user` скачивает Chrome for Testing в каталог данных (`--yes` без терминала и без root делает это сам). Его системные библиотеки (libnss3, libatk, libgbm, libasound, …) и шрифты на Debian / Ubuntu тоже без root: та же команда скачивает их пакеты (`apt-get download` — только загрузка `.deb` из уже настроенных источников apt, индекс должен быть), распаковывает (`dpkg-deb -x`) в `<каталог данных>/chrome/lib` и `chrome/fonts` (со своим `fonts.conf`), а daemon запускает Chrome с `LD_LIBRARY_PATH` и `FONTCONFIG_FILE` на них; зависимости библиотек докачиваются по кругу, пока `ldd` не перестанет находить пропуски (около 7 МБ). На других дистрибутивах библиотеки только из пакетов: `gate4ai browser setup` называет недостающие точно («To run Chrome, install: …»). Xvfb — только пакетом; без него браузер работает headless.

Песочница без root. На Ubuntu 24.04+ AppArmor разрешает непривилегированные user namespaces только программам со своим профилем: у Chrome из пакета Google он есть, у Chrome for Testing в домашнем каталоге — нет, и его песочница не поднимается (Chrome не стартует: «…disabled unprivileged user namespaces with AppArmor…»). Настройка `connectors.browser.sandbox` (`auto` по умолчанию) в этом случае запускает Chrome с `--no-sandbox`, а раздел «Браузер», Настройки → Сервер и `gate4ai doctor` предупреждают: страницу, эксплуатирующую ошибку Chrome, сам Chrome уже не сдержит — обновляйте его (`gate4ai browser setup --user` ставит свежий Stable) и держите список сайтов узким. Root может вернуть песочницу профилем AppArmor для бинарника (`/etc/apparmor.d/`, как у `chrome`) или `sysctl kernel.apparmor_restrict_unprivileged_userns=0`; `sandbox: on` запрещает запуск без неё. Совсем без root и без `--no-sandbox` — образ Docker ниже.

Адрес из интернета: daemon сам регистрируется на relay.gate4.ai и выпускает сертификат на `<id>.relay.gate4.ai` (DNS-01, обычно до двух минут). Relay видит только случайный id устройства и шифротекст, больше с сервера ничего не уходит (README, «Что уходит с компьютера»); установщик печатает это под адресом. Не нужен — `GATE4AI_NO_RELAY=1` при установке или `gate4ai relay off` потом. Если выпуск не удался (лимиты Let's Encrypt, сеть), адрес всё равно поднимается на временном самоподписанном сертификате — браузер предупредит о нём, облачные ассистенты пока не подключатся, — а выпуск повторяется сам (через 5 минут, далее вдвое реже) и заменяет временный сертификат настоящим; `gate4ai ui --print` и `gate4ai relay status` показывают состояние (`remote_cert: temporary`).

Проверка: **Настройки → Сервер** в Web UI (и с телефона по `/ui/settings/server`) — состояние каждого компонента и что сделать: служба и linger, хранилище ключей, адрес из интернета, вход, Telegram, пакет браузера, Chrome, экран, шрифты, агенты через локальный сокет. Из терминала — `gate4ai browser setup --dry-run`, `gate4ai doctor`, после первого `browser.open` — `gate4ai browser sessions`.

Без Xvfb браузер работает без экрана (`--headless=new`, окна 1920×1080 — как на Xvfb и у большинства настоящих пользователей, а не 800×600 по умолчанию Chrome): UA и client hints подменены, но сайты с антибот-защитой замечают headless чаще — Web UI и `doctor` предупреждают. Режим задаёт `connectors.browser.display` (`auto`, `virtual`, `headless`, `host`).

## Docker

Образ `ghcr.io/gate4ai/gate4ai:<версия>` (`Dockerfile` в корне, собирает и подписывает cosign `release.yml`; linux/amd64 и arm64) — gate4ai с Chrome, Xvfb и шрифтами: для сервера без root и без системных библиотек Chrome.

```sh
docker run -d --name gate4ai --restart unless-stopped \
  --security-opt seccomp=unconfined \
  -v gate4ai-data:/data ghcr.io/gate4ai/gate4ai:latest
docker exec gate4ai gate4ai ui --print          # Web UI, пароль, адрес из интернета
docker exec -it gate4ai gate4ai skill > ~/.openclaw/skills/gate4ai/SKILL.md
```

Данные — том `/data` (`GATE4AI_HOME`), ключи — `--keystore file`. Web UI слушает только loopback контейнера: с этого компьютера — `--network host`, с любого устройства — адрес из интернета. Агент на этом же сервере ходит через `GATE4AI_URL=https://<id>.relay.gate4.ai` или через смонтированный `/data/run` (сокет агентов) — `GATE4AI_HOME=<том> gate4ai …`. Проверка образа — `cosign verify ghcr.io/gate4ai/gate4ai:<версия> …` ([SECURITY.md](../SECURITY.md)).

Chrome в Docker: песочнице Chrome нужны пространства имён — `--security-opt seccomp=unconfined` (или профиль seccomp Chrome); иначе Chrome не стартует, а ошибка пакета приводит его слова.

## Почему отдельный пользователь ОС для агента

Агент OpenClaw — это shell. Процесс того же пользователя мог бы читать данные gate4ai и подключиться к Xvfb. Отдельный пользователь закрывает файлы и сокеты (права `0600` / `0700`, Xvfb — без TCP и с cookie). Порта отладки у Chrome нет: CDP идёт по pipe, клиентам пакета — 127.0.0.1 с секретным токеном в адресе, без токена — 404. Альтернатива отдельному пользователю — `gate4ai run openclaw --container`: сокет агента и токен монтируются в контейнер.

Сокет агентов — `0600`, поэтому OpenClaw другого пользователя ходит по `GATE4AI_URL` через relay (лишний круг через интернет, но без открытых портов).

## Безопасность

- **Канал.** relay — passthrough TLS: кадры и ввод relay не видит.
- **Что видит человек.** Вкладку сеанса агента (кадры CDP screencast); агенту экран не отдаётся — свои снимки он берёт `page_screenshot` под политикой. Кадры не пишутся на диск и в журнал.
- **IP сервера.** На VPS теряется «домашний IP» из спецификации: сайты с антифродом (госуслуги, банки) чаще просят капчу или блокируют. Просмотр и handoff закрывают капчу; для жёстких сайтов — сервер дома или исходящий прокси через домашнюю сеть (вне этого дизайна).

## Что дальше (отдельные задачи)

- **Групповой сокет агентов** (`--agent-socket-group openclaw`, права `0660`): агент другого пользователя ходит локально, без relay; опознание агента по процессам другого пользователя не работает — только ключ агента.
- **Полный экран (Xvnc + noVNC)** — если встретятся окна, которых CDP не показывает (печать, меню расширений, системное окно прокси-авторизации).
- **Windows:** CDP по pipe (`--remote-debugging-io-pipes` с наследуемыми дескрипторами) — пока там порт отладки на 127.0.0.1.

## Источники

- OpenClaw, песочница и браузер: [docs.openclaw.ai — Sandboxing](https://docs.openclaw.ai/uk/gateway/sandboxing), [npm openclaw 2026.7.1 — sandboxing.md](https://cdn.jsdelivr.net/npm/openclaw@2026.7.1-2/docs/gateway/sandboxing.md) (noVNC-наблюдатель с паролем и токен-ссылкой, `allowHostControl`, `cdpSourceRange`)
- [CVE-2026-32064](https://cvefeed.io/vuln/detail/CVE-2026-32064) — x11vnc без аутентификации в sandbox-browser OpenClaw до 2026.2.21: почему у нас X-сервер без TCP и с cookie
