// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"context"

	"github.com/gate4ai/sdk-go/abi"

	"github.com/gate4ai/sdk-go/manifest"
)

// egressConnector — внутренний коннектор: решение «пустить ли агента из песочницы
// на домен» проходит обычный pipeline (политика egress.connect, подтверждение,
// квитанция). Исполнять нечего — соединение устанавливает Egress Proxy.
type egressConnector struct{}

func (egressConnector) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "egress", Version: "1", Description: "Network egress of sandboxed agents (internal)", Actions: []manifest.Action{
		{Name: "connect", Class: "external", Description: "Open a network connection from the sandbox",
			Args: []manifest.Arg{{Name: "host", Type: manifest.TString, Required: true}, {Name: "port", Type: manifest.TInteger}}}}}
}

func (egressConnector) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Result: map[string]any{"allowed": r.Args["host"]}}, nil
}

func (egressConnector) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Unverified, Method: "none"}, nil
}
