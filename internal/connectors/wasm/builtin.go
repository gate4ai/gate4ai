// SPDX-License-Identifier: AGPL-3.0-only

package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Builtin — коннектор ядра (runtime: builtin), написанный на PDK коннекторов:
// тот же код, что в WASM-модуле, исполняется в процессе daemon на тех же
// функциях хоста с той же проверкой возможностей манифеста.
type Builtin struct {
	e   *Engine
	man manifest.Manifest
	c   connector.Connector
}

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
func builtinCall[R any](ctx context.Context, b *Builtin, op string, settings map[string]any, req any, ev func(*R) *[]abi.EvidenceItem) (R, error) {
	var res R
	lim := b.man.Limits.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(lim.TimeoutS)*time.Second)
	defer cancel()
	d, err := b.e.Dispatcher(ctx, b.man, lim, settings)
	if err != nil {
		return res, err
	}
	defer d.Close()
	in, _ := json.Marshal(req)
	if err := abi.Decode(connector.HandleContext(ctx, b.man, b.c, d, op, in), &res); err != nil {
		var zero R
		return zero, err
	}
	*ev(&res) = append(*ev(&res), d.Evidence()...)
	return res, nil
}

func (b *Builtin) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	return builtinCall(ctx, b, connector.OpExecute, req.Settings, req, func(r *abi.ExecResult) *[]abi.EvidenceItem { return &r.Evidence })
}

func (b *Builtin) Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error) {
	return builtinCall(ctx, b, connector.OpVerify, req.Settings, req, func(r *abi.VerifyResult) *[]abi.EvidenceItem { return &r.Evidence })
}

func (b *Builtin) Close() error { return nil }
