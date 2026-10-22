# Наблюдаемость relay

Метрики, логи и трейсы relay и сервера, дашборд в Grafana. Стек ставится на тот же сервер, что и relay (relay.gate4.ai, HostUp Nano), через Docker Compose — [deploy/observability](../deploy/observability). Снаружи доступны только Grafana по HTTPS с логином и сайт gate4.ai (тот же Caddy завершает TLS и для него); всё остальное слушает `127.0.0.1`.

```
агент/браузер ──TLS:443──► gate4ai-relay ──SNI grafana.gate4.ai, без расшифровки──► Caddy 127.0.0.1:8444 (TLS, Let's Encrypt) ──► Grafana 127.0.0.1:3000
                               │       SNI gate4.ai, www.gate4.ai ──────────────────────►       то же                       ──► сайт 127.0.0.1:8080
                               │ --metrics 127.0.0.1:9464 ◄── Prometheus 127.0.0.1:9090 ◄── node exporter 127.0.0.1:9100
                               │ --otlp 127.0.0.1:4318    ──► Tempo 127.0.0.1:3200
                               └ stderr → journald         ──► Alloy ──► Loki 127.0.0.1:3100
```

## Что отдаёт relay

Флаги `gate4ai-relay` (по умолчанию всё выключено, поведение relay без них не меняется):

| Флаг | Что делает |
|---|---|
| `--metrics 127.0.0.1:9464` | `GET /metrics` в формате Prometheus на отдельном адресе. Не на порту 443 и только на localhost |
| `--otlp 127.0.0.1:4318` | трейсы OpenTelemetry по OTLP/HTTP (без TLS — коллектор на этом же хосте) |
| `--trace-ratio 0.1` | доля сохраняемых трейсов (выборка по trace id) |

Логи relay пишет в stderr (`log/slog`, logfmt) — их собирает journald, Alloy отправляет их в Loki с меткой `unit="gate4ai-relay.service"`. IP-адреса клиентов relay в журнал не пишет.

### Метрики

| Метрика | Смысл |
|---|---|
| `gate4ai_relay_device_registrations_7d` | **регистрации устройств за последние 7 дней** (`POST /api/devices`) |
| `gate4ai_relay_certificate_orders_7d` | **заказы сертификатов Let's Encrypt устройств за 7 дней**, включая продления |
| `gate4ai_relay_device_registrations_total`, `gate4ai_relay_certificate_orders_total` | те же события накопленным счётчиком |
| `gate4ai_relay_devices`, `gate4ai_relay_devices_online` | зарегистрировано устройств / с открытым туннелем |
| `gate4ai_relay_connections_total{kind}` | входящие TLS-соединения: `api` (сам relay), `device`, `route` (`--route`, сайт и Grafana) |
| `gate4ai_relay_active_connections{kind}` | открытые сейчас |
| `gate4ai_relay_rejected_connections_total{reason}` | сброшенные: `conn_limit` (предел соединений), `bad_hello`, `unknown_host`, `unknown_device`, `device_offline` (в том числе сверх 32 ждущих одно устройство), `stream`, `backend` |
| `gate4ai_relay_passthrough_bytes_total{kind,direction}` | байты passthrough (`in` — от клиента) |
| `gate4ai_relay_tunnel_connects_total` | подключения туннелей устройств |
| `gate4ai_relay_device_wait_seconds` | сколько входящее соединение ждало туннель устройства |
| `gate4ai_relay_http_requests_total{route,code}`, `gate4ai_relay_http_request_duration_seconds{route}` | запросы API по шаблону маршрута (`/tunnel` в длительность не входит) |
| `gate4ai_relay_dns_queries_total{rcode}` | запросы авторитетного DNS; `RATELIMITED` — UDP сверх лимита на адрес, без ответа |
| `gate4ai_relay_build_info{version}` | версия; плюс стандартные `go_*` и `process_*` |

**Регистрации и сертификаты за 7 дней.** Сертификат устройство выпускает само (ACME DNS-01), relay его не видит; зато выпуск на `<id>.relay.gate4.ai` возможен только через TXT `_acme-challenge`, который устройство ставит через `POST /api/acme/txt`. Поэтому заказ сертификата — это постановка TXT; TXT одного устройства в пределах 10 минут (несколько авторизаций, повторы) считаются одним заказом. Время событий и накопленные счётчики хранятся в state-файле relay (`stats`), так что значения переживают перезапуск relay и не зависят от ретенции Prometheus; времена старше 8 дней удаляются. Устройства, зарегистрированные до появления учёта, в окно не попадают.

### Трейсы

Спаны: запрос API (имя — шаблон маршрута, например `POST /api/devices`; `/api/health` не трассируется) и passthrough-соединение (`relay.passthrough device` / `relay.passthrough route` с атрибутами `relay.device` или `relay.host`, ожиданием туннеля и байтами). Содержимое трафика relay не расшифровывает, поэтому в трейсах его нет. Контекст трассировки из входящих запросов relay не принимает: клиенты не влияют на выборку.

## Стек

| Служба | Образ | Адрес | Память (лимит) | Хранение |
|---|---|---|---|---|
| Prometheus | `prom/prometheus:v3.5.0` | 127.0.0.1:9090 | 256m | 15 дней, не больше 2 ГБ |
| node exporter | `prom/node-exporter:v1.9.1` | 127.0.0.1:9100 | 48m | — |
| Loki (один процесс) | `grafana/loki:3.5.3` | 127.0.0.1:3100, gRPC 9095 | 256m | 7 дней |
| Tempo (один процесс) | `grafana/tempo:2.8.2` | 127.0.0.1:3200, OTLP 4317/4318, gRPC 9096 | 256m | 3 дня |
| Alloy (журнал → Loki) | `grafana/alloy:v1.10.2` | 127.0.0.1:12345 | 192m | — |
| Grafana | `grafana/grafana:12.1.1` | 127.0.0.1:3000 | 320m | — |
| Caddy (TLS для Grafana и сайта) | `caddy:2.10.2` | 127.0.0.1:8444 | 64m | сертификаты |

Все контейнеры — в сети хоста (`network_mode: host`), каждый явно слушает `127.0.0.1`, публикации портов Docker нет. Лимиты — потолки: в простое стек занимает 300–550 МБ (на relay.gate4.ai — около 550 МБ из 2 ГБ). Ретенция и лимиты меняются в `.env` (см. [.env.example](../deploy/observability/.env.example)). Логи контейнеров стека идут в journald (и оттуда в Loki), а не в файлы Docker.

Конфигурация — в репозитории:
- [compose.yaml](../deploy/observability/compose.yaml);
- [prometheus/prometheus.yml](../deploy/observability/prometheus/prometheus.yml) — цели relay, node, сам стек;
- [loki/loki.yaml](../deploy/observability/loki/loki.yaml), [tempo/tempo.yaml](../deploy/observability/tempo/tempo.yaml);
- [alloy/config.alloy](../deploy/observability/alloy/config.alloy) — весь журнал systemd (relay, сайт, система, контейнеры) с метками `unit`, `container`, `level`; у relay уровень берётся из строки лога;
- [grafana/provisioning](../deploy/observability/grafana/provisioning) — источники данных (Prometheus, Loki, Tempo со ссылкой из трейса в логи) и дашборд;
- [grafana/dashboards/relay.json](../deploy/observability/grafana/dashboards/relay.json) — дашборд «gate4ai relay» (папка gate4ai): состояние, версия, устройства, регистрации и сертификаты за 7 дней и по дням, соединения, сбросы, трафик, API, DNS, ресурсы relay; CPU, память, диск, сеть, load сервера; логи relay, предупреждения системы, трейсы. Дашборд только для чтения в UI: правки — в JSON в репозитории;
- [caddy/Caddyfile](../deploy/observability/caddy/Caddyfile) — Grafana (`GRAFANA_DOMAIN`) и сайт (`WWW_DOMAIN` и `www.` перед ним → `reverse_proxy 127.0.0.1:8080`, сжатие `zstd`/`gzip`); режим TLS — `GRAFANA_TLS` и `WWW_TLS` (`acme` или `internal`).

**Вход в Grafana:** встроенная аутентификация, анонимный доступ, регистрация и basic auth API выключены, cookie только `Secure`, защита от перебора паролей Grafana включена (по умолчанию). Пароль администратора — в файле `secrets/grafana_admin_password` на сервере (в репозитории его нет). `/metrics` Grafana снаружи закрыт (Caddy отвечает 404).

## Развёртывание на relay.gate4.ai

1. **DNS.** Запись `A grafana.gate4.ai` → IP сервера relay (зона `gate4.ai` — у регистратора, relay обслуживает только `relay.gate4.ai`).
2. **Docker** с плагином compose: `apt install docker.io docker-compose` (Debian 13; на relay.gate4.ai стоит так).
3. **Файлы стека** — каталог `deploy/observability` в `/opt/gate4ai-observability`:
   ```sh
   tar -C deploy/observability --exclude ./.env --exclude ./secrets -cf - . | ssh root@relay.gate4.ai 'mkdir -p /opt/gate4ai-observability && tar -C /opt/gate4ai-observability -xf - --no-same-owner'
   ```
4. **Настройки и пароль** (на сервере):
   ```sh
   cd /opt/gate4ai-observability
   cp .env.example .env            # GRAFANA_DOMAIN=grafana.gate4.ai, GRAFANA_TLS=acme, WWW_DOMAIN=gate4.ai, WWW_TLS=acme
   install -d -m 700 secrets
   openssl rand -base64 24 > secrets/grafana_admin_password
   chmod 644 secrets/grafana_admin_password   # каталог закрыт (700); Grafana в контейнере — uid 472, ей нужно чтение
   ```
   Grafana читает пароль только при первом запуске (создании базы). Сменить потом: `docker compose exec grafana grafana cli admin reset-admin-password '<новый>'`.
5. **Флаги relay** — переменная в `/etc/gate4ai-relay/env` и drop-in для unit (их CI не перезаписывает; CI меняет только бинарник, который должен уже поддерживать эти флаги — [README](../README.md#выкладка-relaygate4ai)):
   ```sh
   echo 'RELAY_OBSERVABILITY=--metrics 127.0.0.1:9464 --otlp 127.0.0.1:4318 --trace-ratio 0.1 --route grafana.gate4.ai=127.0.0.1:8444' >> /etc/gate4ai-relay/env
   # сайт — тоже на Caddy:
   sed -i 's|^RELAY_ROUTES=.*|RELAY_ROUTES=--route gate4.ai=127.0.0.1:8444 --route www.gate4.ai=127.0.0.1:8444|' /etc/gate4ai-relay/env
   mkdir -p /etc/systemd/system/gate4ai-relay.service.d
   # ExecStart — тот же, что в unit (systemctl cat gate4ai-relay), плюс $RELAY_OBSERVABILITY в конце:
   cat > /etc/systemd/system/gate4ai-relay.service.d/observability.conf <<'EOF'
   [Service]
   ExecStart=
   ExecStart=/usr/local/bin/gate4ai-relay --domain relay.gate4.ai --listen :443 --dns :53 --ipv4 136.148.221.24 --ipv6 2a13:7c82:128:48:: --state /var/lib/gate4ai-relay/state.json $RELAY_TLS $RELAY_ROUTES $RELAY_OBSERVABILITY
   EOF
   systemctl daemon-reload && systemctl restart gate4ai-relay
   ```
6. **Запуск:** `docker compose up -d`. Caddy получит сертификаты Let's Encrypt (Grafana, `gate4.ai`, `www.gate4.ai`) через TLS-ALPN-01: проверка приходит на 443, relay пробрасывает её по SNI на Caddy без расшифровки; порт 80 не нужен.
7. **Проверка:**
   ```sh
   docker compose ps                                           # все Up
   curl -s 127.0.0.1:9090/api/v1/targets | grep -o '"health":"[a-z]*"' | sort | uniq -c   # 7 × up
   curl -s 127.0.0.1:9464/metrics | grep registrations_7d
   ss -ltnupH | awk '{print $5}' | grep -vE '^(127\.|\[::1\])'  # снаружи: 443 и 53 (relay), 22 (ssh)
   curl -fsS https://gate4.ai/healthz                          # сайт через relay и Caddy
   curl -sI https://www.gate4.ai/install | grep -i ^location   # 308 на https://gate4.ai/install
   ```
   Затем `https://grafana.gate4.ai` → вход `admin` → дашборд **gate4ai / gate4ai relay**. Снаружи (с другой машины) `curl -m5 http://<IP>:9090` и т. п. не должны соединяться.

**На relay.gate4.ai:** стек в `/opt/gate4ai-observability`, флаги relay — `RELAY_OBSERVABILITY` и drop-in `observability.conf`, пароль администратора — `/opt/gate4ai-observability/secrets/grafana_admin_password`.

### Сайт gate4.ai

Сайт — отдельный Compose-проект `gate4ai-www` в `/opt/gate4ai-www` ([www/deploy/compose.yaml](https://github.com/gate4ai/www/blob/main/deploy/compose.yaml), приватный репозиторий): образ `ghcr.io/gate4ai/www:<sha>`, сеть хоста, только HTTP на `127.0.0.1:8080`, логи — в journald (`container=gate4ai-www-www-1` в Loki). TLS и сертификаты на `gate4.ai` и `www.gate4.ai` — у Caddy этого стека (блок `{$WWW_DOMAIN}` в Caddyfile), relay направляет оба имени по SNI на Caddy: `RELAY_ROUTES=--route gate4.ai=127.0.0.1:8444 --route www.gate4.ai=127.0.0.1:8444` в `/etc/gate4ai-relay/env`. `www.gate4.ai` сайт перенаправляет на `gate4.ai` (308). Выкладывает CI репозитория `www` (`docker compose pull && up -d`, откат на прежний тег, если `https://gate4.ai/healthz` не отвечает); описание — в README сайта.

Обновление стека: поменять версии/конфиги в репозитории, повторить копирование (шаг 3) и `docker compose up -d` (конфиги примонтированы файлами — после их правки без смены образа нужен `docker compose restart <служба>`). Данные — в томах Docker `gate4ai-observability_*`.

## Локальная проверка

Тот же стек на машине разработчика: самоподписанный CA Caddy вместо Let's Encrypt и relay из исходников.

```sh
cd deploy/observability
printf 'GRAFANA_DOMAIN=grafana.localhost\nGRAFANA_TLS=internal\nWWW_DOMAIN=gate4.localhost\nWWW_TLS=internal\n' > .env
install -d -m 700 secrets && printf 'local-pass' > secrets/grafana_admin_password && chmod 644 secrets/grafana_admin_password
docker compose up -d
# relay в user-юните, чтобы его журнал собрал Alloy:
go build -o /tmp/gate4ai-relay ../../cmd/gate4ai-relay
systemd-run --user --unit gate4ai-relay.service /tmp/gate4ai-relay --domain relay.localhost --listen 127.0.0.1:8443 \
  --self-signed --state /tmp/relay-state.json --route grafana.localhost=127.0.0.1:8444 --route gate4.localhost=127.0.0.1:8444 \
  --metrics 127.0.0.1:9464 --otlp 127.0.0.1:4318 --trace-ratio 1
curl -sk --resolve relay.localhost:8443:127.0.0.1 -X POST https://relay.localhost:8443/api/devices   # событие для метрик
# Grafana: https://grafana.localhost:8443 (через relay), сертификат — от CA Caddy
# сайт (из www: go run . — 127.0.0.1:8080): curl -k --resolve gate4.localhost:8443:127.0.0.1 https://gate4.localhost:8443/healthz
docker compose down -v; systemctl --user stop gate4ai-relay; rm -rf .env secrets
```

Валидация конфигов без запуска:

```sh
docker compose config -q
docker run --rm --entrypoint promtool -v $PWD/prometheus:/p:ro prom/prometheus:v3.5.0 check config /p/prometheus.yml
docker run --rm -v $PWD/loki:/l:ro grafana/loki:3.5.3 -config.file=/l/loki.yaml -config.expand-env=true -verify-config
docker run --rm -e GRAFANA_DOMAIN=grafana.localhost -e GRAFANA_TLS=acme -e WWW_DOMAIN=gate4.ai -e WWW_TLS=acme -v $PWD/caddy:/c:ro caddy:2.10.2 caddy validate --config /c/Caddyfile
```
