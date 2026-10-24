// SPDX-License-Identifier: Apache-2.0

// Package hosttest — хост gate4.ai в том же процессе для нативных тестов
// коннекторов: те же функции хоста и те же проверки возможностей
// (hostfuncs, capability), что и в настоящем хосте, но секреты, хранилище
// и поставщики service_call (браузер) — фейки, а файлы — настоящие. Сети у
// коннектора нет: внешний мир — только через service_call.
package hosttest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/capability"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Host — тестовый хост одного коннектора.
type Host struct {
	Manifest manifest.Manifest
	Settings map[string]any
	// Secrets — значения секретов по алиасу (@name).
	Secrets map[string]string
	Home    string
	// Services — вызовы других коннекторов (service_call), обычно фейк ServiceFunc.
	Services hostfuncs.ServiceRouter
	KV       *MemKV
	Timeout  time.Duration

	mu   sync.Mutex
	Logs []string
	// Last — диспетчер последнего вызова (доказательства через evidence_add).
	Last *hostfuncs.Dispatcher
}

// New — тестовый хост.
func New(m manifest.Manifest, settings map[string]any, secrets map[string]string) *Host {
	return &Host{Manifest: m, Settings: settings, Secrets: secrets, KV: &MemKV{}}
}

type secrets map[string]string

func (s secrets) Value(alias string) (string, error) {
	v, ok := s[alias]
	if !ok {
		return "", fmt.Errorf("no secret %s", alias)
	}
	return v, nil
}

// Dispatcher — функции хоста для одного вызова.
func (h *Host) Dispatcher() (*hostfuncs.Dispatcher, error) {
	chk, err := capability.New(h.Manifest, h.Settings, h.Home)
	if err != nil {
		return nil, err
	}
	d := &hostfuncs.Dispatcher{Check: chk, Secrets: secrets(h.Secrets), KV: h.KV, Services: h.Services,
		Timeout: h.Timeout,
		Log:     func(level, msg string) { h.mu.Lock(); h.Logs = append(h.Logs, level+": "+msg); h.mu.Unlock() }}
	h.mu.Lock()
	h.Last = d
	h.mu.Unlock()
	return d, nil
}

func (h *Host) run(c connector.Connector, op, action string, req any, out any) error {
	d, err := h.Dispatcher()
	if err != nil {
		return err
	}
	defer d.Close()
	if d.Services != nil {
		a, _ := h.Manifest.Find(action)
		d.Services = callsOnly{a: a, r: d.Services}
	}
	b, _ := json.Marshal(req)
	return abi.Decode(connector.Handle(h.Manifest, c, d, op, b), out)
}

// Execute исполняет действие (аргументы нормализуются по манифесту, результат
// проверяется по output_schema, как в хосте);
// доказательства evidence_add добавляются к результату.
func (h *Host) Execute(c connector.Connector, action string, args map[string]any) (abi.ExecResult, error) {
	return h.ExecuteRequest(c, abi.ExecRequest{Action: action, Args: args})
}

// ExecuteRequest — Execute с остальными полями запроса хоста (Caller,
// Revealed, Accounts, Login — как их передаёт хост нативному коннектору);
// Settings пустые — настройки хоста.
func (h *Host) ExecuteRequest(c connector.Connector, req abi.ExecRequest) (abi.ExecResult, error) {
	var res abi.ExecResult
	action := req.Action
	a, ok := h.Manifest.Find(action)
	if !ok {
		return res, fmt.Errorf("unknown action %s", action)
	}
	norm, err := a.Normalize(req.Args)
	if err != nil {
		return res, err
	}
	req.Args = norm
	if req.Settings == nil {
		req.Settings = h.Settings
	}
	err = h.run(c, connector.OpExecute, action, req, &res)
	if err != nil {
		return res, err
	}
	if err := abi.CheckResult(res, a.OutputSchema); err != nil {
		return res, err
	}
	res.Evidence = append(res.Evidence, h.Last.Evidence()...)
	return res, nil
}

// Verify проверяет результат; args — аргументы исполненного действия (как есть).
func (h *Host) Verify(c connector.Connector, action string, args map[string]any, ex abi.ExecResult) (abi.VerifyResult, error) {
	var res abi.VerifyResult
	err := h.run(c, connector.OpVerify, action, abi.VerifyRequest{Action: action, Args: args, Settings: h.Settings, ResultRef: ex.ResultRef, Structured: ex.Structured}, &res)
	return res, err
}

// MemKV — хранилище в памяти с квотой.
type MemKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *MemKV) Get(key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok, nil
}

func (k *MemKV) Set(key string, v []byte, quota int64) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string][]byte{}
	}
	total := int64(len(key) + len(v))
	for kk, vv := range k.m {
		if kk != key {
			total += int64(len(kk) + len(vv))
		}
	}
	if total > quota {
		return abi.Errorf(abi.CodeLimit, "storage quota exceeded")
	}
	k.m[key] = append([]byte(nil), v...)
	return nil
}

func (k *MemKV) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}

// ServiceFunc — фейк поставщиков для service_call в тестах коннектора.
type ServiceFunc func(ctx context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error)

// ServiceCall вызывает функцию.
func (f ServiceFunc) ServiceCall(ctx context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	return f(ctx, rule, r)
}

// callsOnly — как хост: из действия — только подвызовы из его calls.
type callsOnly struct {
	a manifest.Action
	r hostfuncs.ServiceRouter
}

func (c callsOnly) ServiceCall(ctx context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	if !c.a.CallsAction(r.Target, r.Action) {
		return abi.ExecResult{}, abi.Errorf(abi.CodeCapabilityDenied, "action %s does not declare the call %s.%s (calls)", c.a.Name, r.Target, r.Action)
	}
	return c.r.ServiceCall(ctx, rule, r)
}
