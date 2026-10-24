// SPDX-License-Identifier: AGPL-3.0-only

// Коннектор для e2e к тестовому сайту gate4ai (e2e/testsite): таблица с
// главной, кабинет со входом по учётке и кнопка в кабинете с проверкой
// результата — всё через действия встроенного браузера (service_call).
package main

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
)

//go:embed manifest.yaml
var manifestYAML []byte

type site struct{}

// resultRe — строка результата в кабинете.
var resultRe = regexp.MustCompile(`Заявка №(\d+) принята`)

type pageText struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// base — адрес сайта: аргумент url, иначе настройка site.
func base(r abi.ExecRequest) (string, error) {
	if u, _ := r.Args["url"].(string); u != "" {
		return strings.TrimSuffix(u, "/"), nil
	}
	if u, _ := r.Settings["site"].(string); u != "" {
		return strings.TrimSuffix(u, "/"), nil
	}
	return "", abi.Errorf(abi.CodeBadRequest, "no site: pass --url or set connectors.testconnector.site")
}

func (site) Execute(_ context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	u, err := base(r)
	if err != nil {
		return abi.ExecResult{}, err
	}
	switch r.Action {
	case "read":
		return read(h, u)
	case "cabinet":
		return cabinet(h, u)
	case "press":
		return press(h, u)
	}
	return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown action %s", r.Action)
}

// read — таблица показаний с главной: строки «Счётчик<TAB>Показание» в тексте страницы.
func read(h *abi.Host, u string) (abi.ExecResult, error) {
	if _, err := h.ServiceCall("browser", "open", map[string]any{"url": u + "/"}, nil); err != nil {
		return abi.ExecResult{}, err
	}
	var p pageText
	if _, err := h.ServiceCall("browser", "observe", map[string]any{"selector": "#readings"}, &p); err != nil {
		return abi.ExecResult{}, err
	}
	rows := []map[string]any{}
	for _, line := range strings.Split(p.Text, "\n") {
		cells := strings.Split(strings.TrimSpace(line), "\t")
		if len(cells) != 2 || cells[0] == "Счётчик" {
			continue
		}
		rows = append(rows, map[string]any{"meter": strings.TrimSpace(cells[0]), "value": strings.TrimSpace(cells[1])})
	}
	var title pageText
	if _, err := h.ServiceCall("browser", "observe", map[string]any{"max_chars": 1}, &title); err != nil {
		return abi.ExecResult{}, err
	}
	return abi.ExecResult{Structured: map[string]any{"title": title.Title, "rows": rows}, Source: "web:" + u + "/"}, nil
}

// openCabinet открывает кабинет; если сайт увёл на вход — вход учёткой слота
// site (значений коннектор не видит). Возвращает статус: already или статус login.
func openCabinet(h *abi.Host, u string) (string, error) {
	var open struct {
		LoginRequired *struct {
			Account string `json:"account"`
		} `json:"login_required"`
	}
	if _, err := h.ServiceCall("browser", "open", map[string]any{"url": u + "/cabinet"}, &open); err != nil {
		return "", err
	}
	if open.LoginRequired == nil {
		return "already", nil
	}
	var login struct {
		Status string `json:"status"`
	}
	if _, err := h.ServiceCall("browser", "login", map[string]any{"account": "site"}, &login); err != nil {
		return "", err
	}
	if login.Status == "still_on_login" {
		return "", abi.Errorf(abi.CodeBadRequest, "sign-in did not succeed: still on the login page")
	}
	return login.Status, nil
}

func cabinet(h *abi.Host, u string) (abi.ExecResult, error) {
	status, err := openCabinet(h, u)
	if err != nil {
		return abi.ExecResult{}, err
	}
	var p pageText
	if _, err := h.ServiceCall("browser", "observe", map[string]any{}, &p); err != nil {
		return abi.ExecResult{}, err
	}
	h.Log("info", "cabinet "+p.URL)
	return abi.ExecResult{Structured: map[string]any{"status": status, "title": p.Title, "text": p.Text}, Source: "web:" + p.URL}, nil
}

// press нажимает кнопку в кабинете и ждёт строку результата на странице.
func press(h *abi.Host, u string) (abi.ExecResult, error) {
	if _, err := openCabinet(h, u); err != nil {
		return abi.ExecResult{}, err
	}
	if _, err := h.ServiceCall("browser", "act", map[string]any{"selector": "#press", "method": "click", "expect_text": "принята"}, nil); err != nil {
		return abi.ExecResult{}, err
	}
	var p pageText
	if _, err := h.ServiceCall("browser", "observe", map[string]any{"selector": "#result"}, &p); err != nil {
		return abi.ExecResult{}, err
	}
	n, line, ok := result(p.Text)
	if !ok {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "no result on the page: %q", p.Text)
	}
	return abi.ExecResult{Structured: map[string]any{"number": n, "result": line}, ResultRef: strconv.Itoa(n)}, nil
}

func result(text string) (int, string, bool) {
	m := resultRe.FindStringSubmatch(text)
	if m == nil {
		return 0, "", false
	}
	n, _ := strconv.Atoi(m[1])
	return n, m[0], true
}

// Verify — проверка press по кабинету: на странице должна быть та же строка
// результата с тем же номером (result_ref).
func (site) Verify(_ context.Context, h *abi.Host, r abi.VerifyRequest) (abi.VerifyResult, error) {
	if r.Action != "press" {
		return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
	}
	u, err := base(abi.ExecRequest{Args: r.Args, Settings: r.Settings})
	if err != nil {
		return abi.VerifyResult{}, err
	}
	if _, err := openCabinet(h, u); err != nil {
		return abi.VerifyResult{}, err
	}
	var p pageText
	if _, err := h.ServiceCall("browser", "observe", map[string]any{"selector": "#result"}, &p); err != nil {
		return abi.VerifyResult{}, err
	}
	n, line, ok := result(p.Text)
	if !ok || strconv.Itoa(n) != r.ResultRef {
		return abi.VerifyResult{Status: abi.NotConfirmed, Method: "cabinet_page", Detail: fmt.Sprintf("the cabinet shows %q, expected request %s", p.Text, r.ResultRef)}, nil
	}
	return abi.VerifyResult{Status: abi.Verified, Method: "cabinet_page", Detail: line}, nil
}

func init() { connector.Register(manifestYAML, site{}) }
func main() {}
