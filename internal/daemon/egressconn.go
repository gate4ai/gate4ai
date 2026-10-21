// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// egressConnector — внутренний коннектор: решение «пустить ли агента из песочницы
// на домен» проходит обычный pipeline (политика egress.connect, подтверждение,
// квитанция). Исполнять нечего — соединение устанавливает Egress Proxy.
type egressConnector struct{}

func (egressConnector) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "egress", Version: "1", Description: "Network egress of sandboxed agents (internal)", Actions: []manifest.Action{
		{Name: "connect", Class: "external", Description: "Open a network connection from the sandbox",
			InputSchema: manifest.MustSchema(`{ type: object, properties: { host: { type: string }, port: { type: integer } }, required: [host] }`)}}}
}

func (egressConnector) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Structured: map[string]any{"allowed": r.Args["host"]}}, nil
}

func (egressConnector) Verify(_ context.Context, r abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
}
