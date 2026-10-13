// SPDX-License-Identifier: FSL-1.1-ALv2

package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gate4ai/gate4ai/internal/approval"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/webui"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/internal/sandbox"
)

// uiAddr — адрес Web UI (только loopback); "" — выключен.
var uiAddr = "127.0.0.1:7431"

// telegramAPI — адрес Bot API (переопределяется для тестов и self-hosted Bot API).
var telegramAPI = "https://api.telegram.org"

// egressAddr — адрес Egress Proxy (с аутентификацией каждого запуска).
var egressAddr = "127.0.0.1:0"

// setup подключает к daemon каналы подтверждений, Web UI, трей и relay.
func setup(ctx context.Context, d *daemon.Daemon, log *slog.Logger) error {
	approval.Attach(ctx, d, approval.NewDesktop(), telegramAPI)
	relayclient.Attach(ctx, d)
	if _, err := sandbox.Attach(ctx, d, paths.RunDir(), egressAddr); err != nil {
		return err
	}
	if uiAddr != "" {
		u := webui.New(d)
		l, err := u.Listen(uiAddr)
		if err != nil {
			return err
		}
		d.AddRoutes(u.OwnerRoutes)
		srv := &http.Server{Handler: u.Handler()}
		go func() { <-ctx.Done(); srv.Close() }()
		go srv.Serve(l)
		log.Info("web UI", "addr", "http://"+u.Addr, "login", "gate4ai ui")
	}
	return nil
}
