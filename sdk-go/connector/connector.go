// SPDX-License-Identifier: Apache-2.0

// Package connector — PDK коннектора gate4.ai на Go. Коннектор реализует
// Execute и Verify, пользуясь только функциями хоста (abi.Host), и
// регистрируется в init() пакета main:
//
//	//go:embed manifest.yaml
//	var manifestYAML []byte
//
//	func init() { connector.Register(manifestYAML, &Mail{}) }
//	func main() {}
//
// Сборка модуля: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o connector.wasm .
// В нативных тестах тот же код исполняется на тестовом хосте (hosttest).
package connector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Connector — реализация действий из манифеста.
type Connector interface {
	Execute(ctx context.Context, h *abi.Host, req abi.ExecRequest) (abi.ExecResult, error)
	Verify(ctx context.Context, h *abi.Host, req abi.VerifyRequest) (abi.VerifyResult, error)
}

var registered struct {
	m manifest.Manifest
	c Connector
}

// Register регистрирует коннектор модуля; манифест проверяется сразу.
func Register(manifestYAML []byte, c Connector) {
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		panic(err)
	}
	registered.m, registered.c = m, c
}

// Операции модуля.
const (
	OpManifest = "manifest"
	OpExecute  = "execute"
	OpVerify   = "verify"
)

// Handle — общий вход экспортов модуля и нативных тестов: операция и запрос
// JSON → Response JSON.
func Handle(m manifest.Manifest, c Connector, t abi.Transport, op string, in []byte) []byte {
	return HandleContext(context.Background(), m, c, t, op, in)
}

// HandleContext — Handle с контекстом вызова (нативный пакет: срок вызова и
// отмена хостом).
func HandleContext(ctx context.Context, m manifest.Manifest, c Connector, t abi.Transport, op string, in []byte) (out []byte) {
	defer func() {
		if r := recover(); r != nil {
			out = abi.Fail(abi.Errorf(abi.CodeConnector, "panic: %v", r))
		}
	}()
	h := &abi.Host{T: t}
	switch op {
	case OpManifest:
		return abi.OK(m)
	case OpExecute:
		var req abi.ExecRequest
		if err := json.Unmarshal(in, &req); err != nil {
			return abi.Fail(abi.Errorf(abi.CodeBadRequest, "%v", err))
		}
		if _, ok := m.Find(req.Action); !ok {
			return abi.Fail(abi.Errorf(abi.CodeBadRequest, "unknown action %q", req.Action))
		}
		res, err := c.Execute(ctx, h, req)
		if err != nil {
			return abi.Fail(err)
		}
		return abi.OK(res)
	case OpVerify:
		var req abi.VerifyRequest
		if err := json.Unmarshal(in, &req); err != nil {
			return abi.Fail(abi.Errorf(abi.CodeBadRequest, "%v", err))
		}
		res, err := c.Verify(ctx, h, req)
		if err != nil {
			return abi.Fail(err)
		}
		return abi.OK(res)
	}
	return abi.Fail(abi.Errorf(abi.CodeBadRequest, "unknown operation %q", op))
}

// Registered — зарегистрированный коннектор (для нативных тестов пакета main).
func Registered() (manifest.Manifest, Connector, error) {
	if registered.c == nil {
		return manifest.Manifest{}, nil, fmt.Errorf("connector: Register was not called")
	}
	return registered.m, registered.c, nil
}
