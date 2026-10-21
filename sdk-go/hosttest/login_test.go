// SPDX-License-Identifier: Apache-2.0

package hosttest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Коннектор входа (сложный вход, учётка с via): действие login {account}
// проходит шаги через действия браузера и вводит секреты browser.fill_secret —
// значений он не видит. Контракт ответа — как у browser.login: {status}.
const loginManifest = `
connector: mos_login
version: "1"
capabilities:
  services:
    - target: browser
      contract: 1
      actions: [observe, act, fill_secret]
      params: { domains: [login.mos.ru] }
credentials:
  - { slot: login, bind: ["https://login.mos.ru"] }
actions:
  - name: login
    class: write
    calls: [browser.observe, browser.act, browser.fill_secret]
    input_schema:
      type: object
      properties:
        account: { type: string }
      required: [account]
    output_schema:
      type: object
      properties:
        status: { type: string, enum: [logged_in, submitted, still_on_login] }
      required: [status]
`

type mosLogin struct{}

func (mosLogin) Execute(_ context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	account, _ := r.Args["account"].(string)
	// До ввода секретов страницу можно смотреть: выбрать способ входа.
	var page struct {
		Text string `json:"text"`
	}
	if _, err := h.ServiceCall("browser", "observe", map[string]any{}, &page); err != nil {
		return abi.ExecResult{}, err
	}
	if strings.Contains(page.Text, "По телефону") {
		if _, err := h.ServiceCall("browser", "act", map[string]any{"selector": "button", "text": "По логину", "method": "click"}, nil); err != nil {
			return abi.ExecResult{}, err
		}
	}
	for _, f := range [][2]string{{"login", "#login"}, {"password", "#password"}} {
		if _, err := h.ServiceCall("browser", "fill_secret", map[string]any{"account": account, "field": f[0], "selector": f[1]}, nil); err != nil {
			return abi.ExecResult{}, err
		}
	}
	if _, err := h.ServiceCall("browser", "act", map[string]any{"selector": "#bind", "method": "click"}, nil); err != nil {
		return abi.ExecResult{}, err
	}
	// Код из SMS вводит человек: вопрос — через канал подтверждений хоста,
	// ответ идёт сразу в поле и коннектору не возвращается.
	if _, err := h.ServiceCall("browser", "act", map[string]any{"selector": "#sms", "method": "wait", "timeout_ms": 5000}, nil); err == nil {
		if _, err := h.ServiceCall("browser", "fill_secret", map[string]any{"account": account, "field": "sms", "selector": "#sms",
			"source": "human", "prompt": "Код из SMS для входа на mos.ru"}, nil); err != nil {
			return abi.ExecResult{}, err
		}
		if _, err := h.ServiceCall("browser", "act", map[string]any{"selector": "#sms", "method": "press", "value": "Enter"}, nil); err != nil {
			return abi.ExecResult{}, err
		}
	}
	var after struct {
		URL string `json:"url"`
	}
	if _, err := h.ServiceCall("browser", "act", map[string]any{"selector": "body", "method": "wait"}, &after); err != nil {
		return abi.ExecResult{}, err
	}
	status := "submitted"
	if strings.HasPrefix(after.URL, "https://lk.emias.mos.ru/") {
		status = "logged_in"
	}
	return abi.ExecResult{Structured: map[string]any{"status": status}}, nil
}

func (mosLogin) Verify(context.Context, *abi.Host, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "no verification")
}

func TestLoginConnectorWithFakeBrowser(t *testing.T) {
	m, err := manifest.Parse([]byte(loginManifest))
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	url := "https://login.mos.ru/sign"
	// Фейковый браузер: значений секретов у коннектора нет — в аргументах
	// только учётка, имя поля и селектор.
	h := New(m, nil, nil)
	h.Services = ServiceFunc(func(_ context.Context, _ manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
		b, _ := json.Marshal(r.Args)
		calls = append(calls, r.Action+" "+string(b))
		switch r.Action {
		case "observe":
			return abi.ExecResult{Structured: map[string]any{"url": url, "title": "Вход", "text": "Войти: По телефону | По логину"}}, nil
		case "act":
			if r.Args["selector"] == "#sms" && r.Args["method"] == "press" {
				url = "https://lk.emias.mos.ru/"
			}
			return abi.ExecResult{Structured: map[string]any{"url": url}}, nil
		case "fill_secret":
			return abi.ExecResult{Structured: map[string]any{"url": url, "origin": "https://login.mos.ru"}}, nil
		}
		return abi.ExecResult{}, abi.Errorf(abi.CodeNotFound, "unexpected %s", r.Action)
	})
	res, err := h.Execute(mosLogin{}, "login", map[string]any{"account": "@emias"})
	if err != nil || res.Structured.(map[string]any)["status"] != "logged_in" {
		t.Fatal(res, err, calls)
	}
	all := strings.Join(calls, "\n")
	for _, want := range []string{`fill_secret {"account":"@emias","field":"password","selector":"#password"}`, `"source":"human"`, `"text":"По логину"`} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in\n%s", want, all)
		}
	}
	// Подвызов вне calls хост не пропускает.
	h.Manifest.Actions[0].Calls = []string{"browser.observe"}
	if _, err := h.Execute(mosLogin{}, "login", map[string]any{"account": "@emias"}); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatal(err)
	}
}
