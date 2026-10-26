// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gate4ai/gate4ai/internal/approval"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/webui"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/internal/sandbox"
)

// uiAddr — адрес Web UI (только loopback); "" — выключен.
var uiAddr = "127.0.0.1:7431"

// resetUILogin — сбросить защиту входа в Web UI при старте (забыт пароль, нет
// Telegram): новый сгенерированный пароль покажет `gate4ai ui`. Обычный путь —
// `gate4ai ui --reset-password` у работающего daemon.
var resetUILogin bool

// telegramAPI — адрес Bot API (переопределяется для тестов и self-hosted Bot API).
var telegramAPI = "https://api.telegram.org"

// egressAddr — адрес Egress Proxy (с аутентификацией каждого запуска).
var egressAddr = "127.0.0.1:0"

// public — прямой публичный адрес без relay (--public …).
var public publicOpts

// relayAuto — доступ из интернета включён по умолчанию: без адреса и без запрета
// daemon сам регистрируется на relay gate4.ai (--relay-auto=false — нет: тесты, стенд).
var relayAuto = true

type publicOpts struct {
	addr, url, cert, key string
}

// setup подключает к daemon публичный адрес, relay, Web UI, каналы подтверждений
// и песочницу. Источники публичного адреса — до каналов подтверждений (вебхук
// Telegram строится от него); прямой адрес важнее relay.
func setup(ctx context.Context, d *daemon.Daemon, log *slog.Logger) error {
	if _, err := servePublic(d, public, log); err != nil {
		return err
	}
	relayStatus := relayclient.Attach(ctx, d, relayAuto)
	u := webui.New(d)
	u.Relay = relayStatus
	// Пароль Web UI есть до того, как Web UI станет доступен — в том числе с
	// публичного адреса: на свежей установке daemon генерирует его сам, на диске
	// только хэш; открытый текст показывают `gate4ai ui`, установщик и трей.
	if resetUILogin {
		if err := u.D.State.Set("ui.login", "{}"); err != nil {
			return err
		}
	}
	if pw, err := u.EnsurePassword(); err != nil {
		return err
	} else if pw != "" {
		log.Info("web UI password generated (shown by gate4ai ui until the first sign-in)")
	}
	d.AddRoutes(u.OwnerRoutes)
	d.AddPublic("/ui/", u.RemoteHandler())
	desktop := approval.NewDesktop()
	if os.Getenv(approval.NotifyEnv) == "log" {
		desktop.Log = log
		log.Info("desktop notifications go to this log and are not shown", approval.NotifyEnv, "log")
	}
	u.Channels = approval.Attach(ctx, d, desktop, telegramAPI)
	if resetUILogin {
		log.Warn("web UI login protection reset (--reset-ui-login): new password — gate4ai ui, Telegram confirmation off")
		// Сброс виден владельцу: если сбросил не он — это сигнал.
		if tg := u.Channels.Telegram(); tg != nil {
			go tg.Notice("⚠️ Защита входа в Web UI gate4.ai сброшена: daemon запущен с --reset-ui-login. Новый пароль показан на компьютере (gate4ai ui), подтверждение входа в Telegram выключено. Если это были не вы — войдите и смените пароль в «Настройки → Вход».")
		}
	}
	if _, err := sandbox.Attach(ctx, d, paths.RunDir(), egressAddr); err != nil {
		return err
	}
	if uiAddr != "" {
		l, err := u.Listen(uiAddr)
		if err != nil {
			return err
		}
		// Останавливается вместе с API daemon (Shutdown), до закрытия баз.
		srv := &http.Server{Handler: u.Handler(), ReadHeaderTimeout: 10 * time.Second}
		d.TrackServer(srv)
		go srv.Serve(l)
		d.SetLocalUI("http://" + u.Addr)
		log.Info("web UI", "addr", "http://"+u.Addr, "login", "gate4ai ui")
	}
	return nil
}

// servePublic — прямой публичный адрес: HTTPS со своим сертификатом или HTTP на
// loopback за reverse proxy этого компьютера. Тот же обработчик, что за relay.
// Возвращает адрес слушателя ("" — выключен).
func servePublic(d *daemon.Daemon, o publicOpts, log *slog.Logger) (string, error) {
	if o.addr == "" {
		return "", nil
	}
	pu, err := url.Parse(o.url)
	if err != nil || pu.Scheme != "https" || pu.Host == "" || (pu.Path != "" && pu.Path != "/") {
		return "", fmt.Errorf("--public needs --public-url https://<host>[:port] (the address clients use)")
	}
	l, err := net.Listen("tcp", o.addr)
	if err != nil {
		return "", fmt.Errorf("public listener: %w", err)
	}
	external := ""
	switch {
	case o.cert != "" && o.key != "":
		cert, err := tls.LoadX509KeyPair(o.cert, o.key)
		if err != nil {
			l.Close()
			return "", fmt.Errorf("public certificate: %w", err)
		}
		l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	case o.cert != "" || o.key != "":
		l.Close()
		return "", fmt.Errorf("--public-cert and --public-key go together")
	default:
		// Без сертификата TLS снимает reverse proxy: только он может достучаться.
		if ap, ok := l.Addr().(*net.TCPAddr); !ok || !ap.IP.IsLoopback() {
			l.Close()
			return "", fmt.Errorf("--public without --public-cert/--public-key listens only on loopback (behind a reverse proxy), got %s", o.addr)
		}
		external = o.url
	}
	d.AddPublicURL(func() string { return o.url })
	d.ServePublic(l, external)
	log.Info("public address", "url", o.url, "listen", l.Addr().String(), "tls", external == "")
	return l.Addr().String(), nil
}
