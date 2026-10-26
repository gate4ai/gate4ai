// SPDX-License-Identifier: AGPL-3.0-only

package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Builtin — компонент ядра (runtime: builtin: файлы, браузер), написанный на
// PDK коннекторов: тот же код, что в WASM-модуле, исполняется в процессе
// daemon на тех же функциях хоста с той же проверкой возможностей манифеста.
// Значения алиасов из capabilities.reveal хост передаёт в запросе (Revealed).
type Builtin struct {
	e   *Engine
	man manifest.Manifest
	c   connector.Connector
}

// CallEnder — коннектор с состоянием на время корневого вызова (страница
// браузера): EndCall освобождает его.
type CallEnder interface{ EndCall(call string) }

// OwnerRequests — коннектор ядра отвечает на запросы владельца (сеансы
// браузера, просмотр, управление). Их шлёт только daemon и только от имени
// владельца (Web UI, CLI владельца): политики агентов к ним не относятся.
type OwnerRequests interface {
	Owner(ctx context.Context, op string, args json.RawMessage) (any, error)
}

// Evented — коннектор ядра шлёт события хосту (кадры просмотра сеанса):
// SetEmit даёт ему emit; обработчик не должен ждать.
type Evented interface {
	SetEmit(emit func(kind string, data any))
}

// ErrNoOwner — у коннектора нет запросов владельца.
var ErrNoOwner = errors.New("the connector takes no owner requests")

// Builtin оборачивает коннектор ядра; манифест проверяется сразу.
func (e *Engine) Builtin(m manifest.Manifest, c connector.Connector) (*Builtin, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.Runtime != manifest.RuntimeBuiltin {
		return nil, fmt.Errorf("connector %s: runtime %q is not builtin", m.Connector, m.Runtime)
	}
	return &Builtin{e: e, man: m, c: c}, nil
}

func (b *Builtin) Manifest() manifest.Manifest { return b.man }

// builtinCall исполняет op коннектора ядра; результат дополняется
// доказательствами хоста (ev — поле Evidence результата). При ошибке —
// пустой результат, без частично разобранного ответа.
func builtinCall[R any](ctx context.Context, b *Builtin, op string, settings map[string]any, req any, prep func(*hostfuncs.Dispatcher) error, ev func(*R) *[]abi.EvidenceItem) (R, error) {
	var res R
	lim := b.man.Limits.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(lim.TimeoutS)*time.Second)
	defer cancel()
	d, err := b.e.Dispatcher(ctx, b.man, lim, settings)
	if err != nil {
		return res, err
	}
	defer d.Close()
	if prep != nil {
		if err := prep(d); err != nil {
			return res, err
		}
	}
	in, _ := json.Marshal(req)
	if err := abi.Decode(connector.HandleContext(ctx, b.man, b.c, d, op, in), &res); err != nil {
		var zero R
		return zero, err
	}
	*ev(&res) = append(*ev(&res), d.Evidence()...)
	return res, nil
}

func (b *Builtin) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	// Запрос маршалится после prep: значения reveal попадают в него и в маски диспетчера.
	return builtinCall(ctx, b, connector.OpExecute, req.Settings, &req, func(d *hostfuncs.Dispatcher) error { return b.reveal(d, &req) },
		func(r *abi.ExecResult) *[]abi.EvidenceItem { return &r.Evidence })
}

func (b *Builtin) Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error) {
	return builtinCall(ctx, b, connector.OpVerify, req.Settings, req, nil, func(r *abi.VerifyResult) *[]abi.EvidenceItem { return &r.Evidence })
}

// reveal добавляет в запрос значения алиасов capabilities.reveal (ключ модели
// браузера) к тем, что уже положил pipeline (поля учётки для browser.login);
// диспетчер запоминает их и маскирует в доказательствах и логах.
func (b *Builtin) reveal(d *hostfuncs.Dispatcher, req *abi.ExecRequest) error {
	for _, alias := range d.Check.R.Reveal {
		v, err := d.Reveal(alias)
		if err != nil {
			return err
		}
		if req.Revealed == nil {
			req.Revealed = map[string]string{}
		}
		req.Revealed[alias] = v
	}
	return nil
}

// Owner — запрос владельца op к коннектору ядра (ErrNoOwner — он их не
// принимает); args уходят JSON, ответ разбирается в out (nil — не нужен).
func (b *Builtin) Owner(ctx context.Context, op string, args, out any) error {
	o, ok := b.c.(OwnerRequests)
	if !ok {
		return fmt.Errorf("connector %s: %w", b.man.Connector, ErrNoOwner)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(b.man.Limits.WithDefaults().TimeoutS)*time.Second)
	defer cancel()
	res, err := o.Owner(ctx, op, raw)
	if err != nil {
		return fmt.Errorf("connector %s: %s: %w", b.man.Connector, op, err)
	}
	if out == nil {
		return nil
	}
	buf, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

// SetEventSink — куда идут события коннектора ядра (nil — никуда); коннектор
// без событий его не замечает.
func (b *Builtin) SetEventSink(f func(kind string, data json.RawMessage)) {
	e, ok := b.c.(Evented)
	if !ok {
		return
	}
	if f == nil {
		e.SetEmit(nil)
		return
	}
	e.SetEmit(func(kind string, data any) {
		raw, err := json.Marshal(data)
		if err != nil {
			return
		}
		f(kind, raw)
	})
}

// PageURL — адрес открытой страницы браузера (pipeline.URLResolver); "" — не браузер.
func (b *Builtin) PageURL(session, call string) string {
	if r, ok := b.c.(interface {
		PageURL(session, call string) string
	}); ok {
		return r.PageURL(session, call)
	}
	return ""
}

// CallAdopter — коннектор, который умеет отдать страницу корневого вызова
// агенту сеансом (браузер): handoff_to_agent.
type CallAdopter interface {
	AdoptCall(call, agent, instance string) (session, url string, ok bool)
}

// AdoptCall — страница корневого вызова становится сеансом агента (CallAdopter).
func (b *Builtin) AdoptCall(call, agent, instance string) (string, string, bool) {
	if a, ok := b.c.(CallAdopter); ok {
		return a.AdoptCall(call, agent, instance)
	}
	return "", "", false
}

// EndCall — корневое действие закончилось: коннектор освобождает его состояние.
func (b *Builtin) EndCall(call string) {
	if e, ok := b.c.(CallEnder); ok {
		e.EndCall(call)
	}
}

// Close останавливает коннектор, если ему есть что останавливать (Chrome браузера).
func (b *Builtin) Close() error {
	if c, ok := b.c.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}
