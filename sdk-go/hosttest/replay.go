// SPDX-License-Identifier: Apache-2.0

package hosttest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Step — шаг записи поставщика: действие, аргументы и что оно вернуло. Такие
// шаги отдаёт browser.close по сеансу агента — их сохраняют в фикстуру теста.
type Step struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args,omitempty"`
	Result map[string]any `json:"result,omitempty"`
	Error  *abi.Error     `json:"error,omitempty"`
	// Via — шаг коннектора входа, вызванного ради входа (browser.login
	// учётки с via): в записи сеанса его шаги идут вместе с шагом login.
	Via string `json:"via,omitempty"`
}

// Fixture — записанные шаги поставщика: результат browser.close ({session,
// steps}) или ответ gate4ai на него целиком (шаги — в result).
type Fixture struct {
	Steps []Step `json:"steps"`
}

// LoadFixture читает фикстуру из JSON-файла.
func LoadFixture(path string) (Fixture, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, err
	}
	var f struct {
		Fixture
		Result *Fixture `json:"result"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return Fixture{}, fmt.Errorf("fixture %s: %w", path, err)
	}
	if len(f.Steps) == 0 && f.Result != nil {
		return *f.Result, nil
	}
	return f.Fixture, nil
}

// replayIgnored — аргументы сеанса агента: в вызовах коннектора их нет.
var replayIgnored = map[string]bool{"session": true}

// Replay — фейк поставщика target по записанным шагам: на каждый service_call
// — первый неиспользованный шаг с тем же действием и теми же аргументами
// (без session), иначе первый неиспользованный с тем же действием; шагов не
// осталось — ошибка not_found. Шаг с error возвращает эту ошибку.
func Replay(target string, f Fixture) ServiceFunc { return replay(target, f, "") }

// ReplayVia — Replay для коннектора входа via: только его шаги из записи
// сеанса вызывающего (Step.Via).
func ReplayVia(target string, f Fixture, via string) ServiceFunc { return replay(target, f, via) }

// replay — шаги с Via == via: у вызывающего — без шагов коннектора входа (его
// вход — один шаг login), у коннектора входа — только его шаги.
func replay(target string, all Fixture, via string) ServiceFunc {
	var f Fixture
	for _, s := range all.Steps {
		if s.Via == via {
			f.Steps = append(f.Steps, s)
		}
	}
	var mu sync.Mutex
	used := make([]bool, len(f.Steps))
	return func(_ context.Context, _ manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
		if r.Target != target {
			return abi.ExecResult{}, abi.Errorf(abi.CodeNotFound, "replay: no fixture for %s.%s", r.Target, r.Action)
		}
		mu.Lock()
		defer mu.Unlock()
		pick := -1
		for i, s := range f.Steps {
			if used[i] || s.Action != r.Action {
				continue
			}
			if sameArgs(s.Args, r.Args) {
				pick = i
				break
			}
			if pick < 0 {
				pick = i
			}
		}
		if pick < 0 {
			return abi.ExecResult{}, abi.Errorf(abi.CodeNotFound, "replay: the fixture has no more %s.%s steps", target, r.Action)
		}
		used[pick] = true
		s := f.Steps[pick]
		if s.Error != nil {
			return abi.ExecResult{}, &abi.Error{Code: s.Error.Code, Message: s.Error.Message}
		}
		res := abi.ExecResult{}
		if s.Result != nil {
			res.Structured = s.Result
		}
		return res, nil
	}
}

// sameArgs сравнивает аргументы после JSON (числа — float64), без
// replayIgnored и без пустых значений ("", 0, false, пустые списки): CLI
// передаёт значения по умолчанию, а коннектор их не передаёт.
func sameArgs(a, b map[string]any) bool {
	norm := func(m map[string]any) map[string]any {
		raw, _ := json.Marshal(m)
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		out := map[string]any{}
		for k, x := range v {
			if !replayIgnored[k] && !zero(x) {
				out[k] = x
			}
		}
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

func zero(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case float64:
		return t == 0
	case bool:
		return !t
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}
