# gate4ai relay

Relay gate4.ai даёт устройству пользователя публичный HTTPS-адрес `https://<device-id>.<домен>`, когда до daemon `gate4ai` нельзя дойти напрямую (NAT, нет своего домена и сертификата).

**Relay необязателен.** Если у вас уже есть HTTPS до daemon (свой домен, обратный прокси, Tailscale Funnel и т. п.), relay не нужен.

Что делает:
- держит исходящий туннель устройства (WebSocket поверх TLS:443 + yamux);
- пробрасывает входящие TLS-соединения агентов по SNI **без расшифровки**: сертификат выпущен на устройство, ключ есть только у устройства;
- авторитетный DNS своей зоны: A/AAAA на себя для всех устройств, TXT `_acme-challenge.<id>` для ACME DNS-01 (устройство ставит TXT только для своего имени);
- по `--route host=addr` пробрасывает по SNI и сторонние имена (например, сайт `gate4.ai`) на локальный бэкенд — тоже без расшифровки, сертификат держит бэкенд;
- свидетель журнала: хранит подписанные чекпоинты (только хэши) и отвергает уменьшение `seq`.

Протокол — [docs/protocol.md](docs/protocol.md). Клиент устройства — в ядре: `github.com/gate4ai/gate4ai/internal/relayclient` (`gate4ai relay register`).

## Запуск

```sh
go build -o gate4ai-relay ./cmd/gate4ai-relay
gate4ai-relay --domain relay.example.org --listen :443 --dns :53 --ipv4 203.0.113.7 \
  --cert relay.crt --key relay.key --state /var/lib/gate4ai-relay/state.json
```

Сайт на том же сервере и порту 443: `--route example.org=127.0.0.1:8443` (повторяемый флаг; `--route example.org=127.0.0.1:8443,proxy` — бэкенду заголовок PROXY protocol v2 с настоящим адресом клиента). TLS завершает веб-сервер на `127.0.0.1:8443`; сертификат он может получать через ACME TLS-ALPN-01 — проверка проходит через relay. На relay.gate4.ai так подключены `gate4.ai`, `www.gate4.ai` и `grafana.gate4.ai`: TLS для них завершает Caddy на `127.0.0.1:8444` ([docs/observability.md](docs/observability.md#сайт-gate4ai)).

Пределы против злоупотреблений (значения по умолчанию подходят hosted relay): `--reg-per-ip` и `--reg-per-day` — регистраций в сутки с адреса и всего, `--max-devices` — устройств всего, `--cert-orders-per-device` — заказов сертификата на устройство в сутки, `--max-conns` и `--max-conns-per-ip` — одновременных соединений; `--caa` — CA, которым разрешён выпуск в зоне (CAA). Подробно — [docs/protocol.md](docs/protocol.md). Состояние: `--state` (устройства, TXT, счётчики; пишется атомарно с fsync) и каталог `<state>.witness/` рядом — чекпоинты свидетеля, по файлу на устройство.

Наблюдаемость: `--metrics 127.0.0.1:9464` (Prometheus `/metrics`, только localhost), `--otlp 127.0.0.1:4318` и `--trace-ratio` (трейсы OpenTelemetry); логи — в stderr. Стек Grafana + Prometheus + Loki + Tempo для сервера relay и дашборд — [docs/observability.md](docs/observability.md).

Зону `relay.example.org` делегируйте (NS) на этот сервер. Для разработки — `--self-signed`, на устройстве `relay.self_signed: true` и `relay.ca_file`.

## Выкладка relay.gate4.ai

Каждый релиз gate4ai (тег `v*`) выкладывается на сервер relay.gate4.ai: задача `deploy-relay` в [release.yml](../.github/workflows/release.yml) берёт `gate4ai-relay` linux/amd64 из опубликованного релиза, копирует его по SSH и запускает [deploy/remote-install.sh](deploy/remote-install.sh). Скрипт заменяет только бинарник: unit systemd и `/etc/gate4ai-relay/env` (сертификат, `--route` сайта) остаются как настроены на сервере. Если после перезапуска API relay не отвечает, возвращается прежний бинарник. Перезапуск обрывает туннели устройств на пару секунд, устройства переподключаются сами.

Настройки — в окружении `production` репозитория: переменная `DEPLOY_HOST`, секреты `DEPLOY_SSH_KEY` (ключ, открытая часть которого — в `authorized_keys` root на сервере) и `DEPLOY_KNOWN_HOSTS`. **Без `DEPLOY_HOST` задача пропускается молча**, релиз при этом зелёный, а на сервере остаётся старый relay (так в октябре 2026 устройства неделю не получали сертификаты: старый relay отдавал SOA у каждого имени, и certmagic не находил NS зоны). Архивы `gate4ai-relay` для self-hosted — в том же релизе.

Проверка после релиза: в прогоне workflow `release` задача `deploy-relay` должна быть `success`, а не `skipped`; на сервере `gate4ai-relay --version` равна тегу. Настройка окружения (один раз):

```sh
ssh-keygen -t ed25519 -N '' -C 'github-actions deploy-relay' -f id_deploy
ssh root@relay.gate4.ai 'cat >> /root/.ssh/authorized_keys' < id_deploy.pub
gh variable set DEPLOY_HOST --env production --body relay.gate4.ai
gh secret set DEPLOY_SSH_KEY --env production < id_deploy
ssh-keyscan -t ed25519 relay.gate4.ai | gh secret set DEPLOY_KNOWN_HOSTS --env production
gh variable list --env production   # DEPLOY_HOST есть
```

Вручную (например, пока окружение не настроено) выкладывается так же, как в CI: скачать `gate4ai-relay_<версия>_linux_amd64.tar.gz` из релиза, `scp` бинарник в каталог `mktemp -d` на сервере и выполнить `ssh root@relay.gate4.ai "sh -s -- <каталог>/gate4ai-relay.new" < relay/deploy/remote-install.sh`.

Признак отставшего relay на сервере: `dig NS relay.gate4.ai @relay.gate4.ai` пустой или `dig SOA _acme-challenge.x.relay.gate4.ai @relay.gate4.ai` отдаёт SOA в answer (а не NODATA с SOA зоны в authority) — устройства тогда сидят на временном самоподписанном сертификате с ошибкой `could not determine authoritative nameservers`.

## Лицензия

[AGPL-3.0-only](../LICENSE): изменённый relay, доступный пользователям по сети, обязан предоставлять им свой исходный код. Вклад — по CLA и DCO.
