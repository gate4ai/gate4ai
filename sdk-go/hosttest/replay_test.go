// SPDX-License-Identifier: Apache-2.0

package hosttest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

func TestReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.json")
	// Так выглядит результат browser.close: session и шаги сеанса агента.
	if err := os.WriteFile(path, []byte(`{"session":"s1","steps":[
	  {"action":"open","args":{"url":"https://app.example.org/","session":"s1"},"result":{"url":"https://app.example.org/","title":"Кабинет","session":"s1"}},
	  {"action":"observe","args":{"session":"s1"},"result":{"url":"https://app.example.org/","title":"Кабинет","text":"Долг 10"}},
	  {"action":"act","args":{"selector":"#pay","method":"click"},"error":{"code":"not_found","message":"element not found: #pay"}},
	  {"action":"observe","args":{"max_chars":100},"result":{"url":"https://app.example.org/","title":"Кабинет","text":"Долг 0"}}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	// Ответ `gate4ai browser close` целиком: шаги — в result.
	env := filepath.Join(t.TempDir(), "env.json")
	os.WriteFile(env, []byte(`{"status":"ok","action":"browser.close","result":{"session":"s1","steps":[{"action":"open"}]}}`), 0o600)
	if fe, err := LoadFixture(env); err != nil || len(fe.Steps) != 1 || fe.Steps[0].Action != "open" {
		t.Fatal(fe, err)
	}
	call := Replay("browser", f)
	do := func(action string, args map[string]any) (abi.ExecResult, error) {
		return call(context.Background(), manifest.ServiceRule{}, abi.ServiceCallRequest{Target: "browser", Action: action, Args: args})
	}
	if res, err := do("open", map[string]any{"url": "https://app.example.org/"}); err != nil || res.Structured.(map[string]any)["title"] != "Кабинет" {
		t.Fatal(res, err)
	}
	// Совпадение аргументов важнее порядка: max_chars 100 — второй observe.
	if res, err := do("observe", map[string]any{"max_chars": 100}); err != nil || res.Structured.(map[string]any)["text"] != "Долг 0" {
		t.Fatal(res, err)
	}
	if res, err := do("observe", nil); err != nil || res.Structured.(map[string]any)["text"] != "Долг 10" {
		t.Fatal(res, err)
	}
	if _, err := do("act", map[string]any{"selector": "#pay", "method": "click"}); err == nil || !strings.HasPrefix(err.Error(), abi.CodeNotFound) {
		t.Fatal("recorded error:", err)
	}
	if _, err := do("observe", nil); err == nil || !strings.Contains(err.Error(), "no more browser.observe") {
		t.Fatal("steps are used once:", err)
	}
	// Значения по умолчанию из CLI в записи не мешают сопоставлению по аргументам.
	cli := Replay("browser", Fixture{Steps: []Step{
		{Action: "act", Args: map[string]any{"selector": "#a", "method": "click", "text": "", "timeout_ms": 0.0}, Result: map[string]any{"url": "a"}},
		{Action: "act", Args: map[string]any{"selector": "#b", "method": "click", "text": "", "timeout_ms": 0.0}, Result: map[string]any{"url": "b"}},
	}})
	res, err := cli(context.Background(), manifest.ServiceRule{}, abi.ServiceCallRequest{Target: "browser", Action: "act", Args: map[string]any{"selector": "#b", "method": "click"}})
	if err != nil || res.Structured.(map[string]any)["url"] != "b" {
		t.Fatal("match by args, not by order:", res, err)
	}
	// Вход через коннектор входа: у вызывающего — шаг login, у коннектора входа — его шаги.
	withVia := Fixture{Steps: []Step{
		{Action: "fill_secret", Args: map[string]any{"field": "password"}, Result: map[string]any{"origin": "https://login.example"}, Via: "site_login"},
		{Action: "login", Args: map[string]any{"account": "@site"}, Result: map[string]any{"status": "logged_in"}},
	}}
	if _, err := Replay("browser", withVia)(context.Background(), manifest.ServiceRule{}, abi.ServiceCallRequest{Target: "browser", Action: "fill_secret"}); err == nil {
		t.Fatal("the caller's replay has no sign-in connector steps")
	}
	if res, err := ReplayVia("browser", withVia, "site_login")(context.Background(), manifest.ServiceRule{}, abi.ServiceCallRequest{Target: "browser", Action: "fill_secret", Args: map[string]any{"field": "password"}}); err != nil || res.Structured == nil {
		t.Fatal(res, err)
	}
	if _, err := call(context.Background(), manifest.ServiceRule{}, abi.ServiceCallRequest{Target: "mail", Action: "read"}); err == nil {
		t.Fatal("other target")
	}
}
