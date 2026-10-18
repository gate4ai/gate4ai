// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/connectors/store"
)

// MaxConnectorLog — сколько последних строк лога daemon хранит по коннектору.
const MaxConnectorLog = 300

// LogLine — строка лога коннектора (log() коннектора, stderr нативного
// пакета, сообщения daemon о нём).
type LogLine struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// ConnectorLogs — последние строки лога daemon по коннектору (атрибут
// "connector" записи): для `gate4ai connectors logs` и ответа своей сборки.
type ConnectorLogs struct {
	mu  sync.Mutex
	buf map[string][]LogLine
}

// Add добавляет строку.
func (l *ConnectorLogs) Add(conn string, line LogLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf == nil {
		l.buf = map[string][]LogLine{}
	}
	b := append(l.buf[conn], line)
	if len(b) > MaxConnectorLog {
		b = b[len(b)-MaxConnectorLog:]
	}
	l.buf[conn] = b
}

// Lines — строки коннектора не раньше since, последние limit (0 — все).
func (l *ConnectorLogs) Lines(conn string, since time.Time, limit int) []LogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []LogLine{}
	for _, x := range l.buf[conn] {
		if !x.Time.Before(since) {
			out = append(out, x)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Handler — обработчик slog поверх next, который копирует записи с атрибутом
// "connector" в журнал коннектора.
func (l *ConnectorLogs) Handler(next slog.Handler) slog.Handler {
	return &connLogHandler{logs: l, next: next}
}

type connLogHandler struct {
	logs  *ConnectorLogs
	next  slog.Handler
	attrs []slog.Attr
}

func (h *connLogHandler) Enabled(ctx context.Context, lv slog.Level) bool {
	return lv >= slog.LevelDebug // строки коннектора нужны и тогда, когда лог daemon их не пишет
}

func (h *connLogHandler) Handle(ctx context.Context, r slog.Record) error {
	conn := ""
	var rest []string
	add := func(a slog.Attr) {
		if a.Key == "connector" {
			conn = a.Value.String()
			return
		}
		rest = append(rest, a.Key+"="+fmt.Sprint(a.Value.Any()))
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	if conn != "" {
		msg := r.Message
		if len(rest) > 0 {
			msg += " " + strings.Join(rest, " ")
		}
		h.logs.Add(conn, LogLine{Time: r.Time, Level: strings.ToLower(r.Level.String()), Msg: msg})
	}
	if !h.next.Enabled(ctx, r.Level) {
		return nil
	}
	return h.next.Handle(ctx, r)
}

func (h *connLogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &connLogHandler{logs: h.logs, next: h.next.WithAttrs(as), attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
}

func (h *connLogHandler) WithGroup(name string) slog.Handler {
	return &connLogHandler{logs: h.logs, next: h.next.WithGroup(name), attrs: h.attrs}
}

// devLog — строки лога за время вызова для коннектора своей сборки (--dev,
// из папки с исходниками): их видит и агент, который этот коннектор пишет.
// Для остальных коннекторов — ничего: лог — диагностика владельца.
func (d *Daemon) devLog(conn string, since time.Time) []string {
	info, err := (store.Options{Dir: d.ConnectorsDir}).Info(conn)
	if err != nil || info.Trust != store.TrustLocal {
		return nil
	}
	var out []string
	for _, l := range d.ConnLogs.Lines(conn, since, 50) {
		out = append(out, l.Level+": "+l.Msg)
	}
	return out
}
