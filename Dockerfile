# gate4ai с браузером агентов в одном образе: для серверов без root и без системных
# библиотек Chrome (docs/headless-server.md, «Docker»). Публикуется release.yml в
# ghcr.io/gate4ai/gate4ai:<версия> и подписывается cosign.
#
#   docker run -d --name gate4ai --restart unless-stopped \
#     --security-opt seccomp=unconfined \          # песочнице Chrome нужны пространства имён
#     -v gate4ai-data:/data ghcr.io/gate4ai/gate4ai:latest
#   docker exec gate4ai gate4ai ui --print          # Web UI, пароль, адрес из интернета
#
# Web UI слушает только loopback контейнера: с этого компьютера он доступен при
# --network host, с любого устройства — по адресу из интернета (relay).
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/gate4ai/gate4ai/internal/platform/version.Version=${VERSION}" -o /out/gate4ai ./cmd/gate4ai

FROM debian:bookworm-slim
ARG TARGETARCH
# Chrome: Google Chrome под amd64 (deb тянет свои библиотеки), Chromium под arm64;
# Xvfb — виртуальный экран, Noto — шрифты.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl xvfb fonts-noto-core fonts-noto-color-emoji; \
    if [ "$TARGETARCH" = amd64 ]; then \
      curl -fsSLo /tmp/chrome.deb https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb; \
      apt-get install -y --no-install-recommends /tmp/chrome.deb; rm -f /tmp/chrome.deb; \
    else \
      apt-get install -y --no-install-recommends chromium; \
    fi; \
    rm -rf /var/lib/apt/lists/*; \
    useradd -m -u 1000 gate4ai; \
    install -d -o gate4ai -g gate4ai /data
COPY --from=build /out/gate4ai /usr/local/bin/gate4ai
USER gate4ai
ENV GATE4AI_HOME=/data
VOLUME /data
EXPOSE 7431
HEALTHCHECK --interval=30s --timeout=5s CMD gate4ai status >/dev/null || exit 1
ENTRYPOINT ["gate4ai"]
CMD ["daemon", "--keystore", "file"]
