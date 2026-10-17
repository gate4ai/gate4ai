// SPDX-License-Identifier: AGPL-3.0-only

package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/connector"
	"github.com/gate4ai/sdk-go/manifest"
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

func (b *Builtin) run(ctx context.Context, op string, settings map[string]any, req, out any) ([]abi.EvidenceItem, error) {
	lim := b.man.Limits.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(lim.TimeoutS)*time.Second)
	defer cancel()
	d, err := b.e.dispatcher(ctx, b.man, lim, settings)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	in, _ := json.Marshal(req)
	if err := abi.Decode(connector.Handle(b.man, b.c, d, op, in), out); err != nil {
		return nil, err
	}
	return d.Evidence(), nil
}

func (b *Builtin) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	var res abi.ExecResult
	ev, err := b.run(ctx, connector.OpExecute, req.Settings, req, &res)
	if err != nil {
		return abi.ExecResult{}, err
	}
	res.Evidence = append(res.Evidence, ev...)
	return res, nil
}

func (b *Builtin) Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error) {
	var res abi.VerifyResult
	ev, err := b.run(ctx, connector.OpVerify, req.Settings, req, &res)
	if err != nil {
		return abi.VerifyResult{}, err
	}
	res.Evidence = append(res.Evidence, ev...)
	return res, nil
}

func (b *Builtin) Close() error { return nil }
