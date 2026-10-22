// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
)

// ServiceCall — service_call коннектора caller (wasm.Router): вложенный вызов
// через Action Pipeline.
func (d *Daemon) ServiceCall(ctx context.Context, caller string, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	if d.Pipeline == nil {
		return abi.ExecResult{}, abi.Errorf(abi.CodeNotSupported, "daemon is starting")
	}
	return d.Pipeline.ExecuteNested(ctx, caller, rule, r)
}

// callEnder — коннектор с состоянием на время корневого действия (нативный процесс).
type callEnder interface{ EndCall(root string) }

// endCall — корневое действие закончилось: поставщики освобождают его состояние.
func (d *Daemon) endCall(root string) {
	d.Registry.Each(func(c connectors.Client) {
		if e, ok := c.(callEnder); ok {
			e.EndCall(root)
		}
	})
}
