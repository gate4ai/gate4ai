// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser/worker"
)

// waitOf — ожидание из аргументов open / observe / reload; from — адрес, уход
// с которого на страницу входа прерывает ожидание.
func waitOf(a map[string]any, from string) worker.Wait {
	return worker.Wait{Selector: str(a, "wait_for_selector"), Text: str(a, "wait_for_text"), URL: str(a, "wait_for_url"),
		TimeoutMS: num(a, "timeout_ms"), From: from}
}

// bodyFields — тело ответа в результат: json или body, тип, размер.
func bodyFields(out map[string]any, b worker.Body) {
	if b.ContentType != "" {
		out["content_type"] = b.ContentType
	}
	if b.JSON != nil {
		out["json"] = b.JSON
	} else if b.Body != "" {
		out["body"] = b.Body
	}
	if b.Truncated {
		out["truncated"] = true
	}
	if b.Hint != "" {
		out["hint"] = b.Hint
	}
}

// rawResult — документ не HTML (JSON, текст) в ответе read / observe.
func rawResult(u, title string, b worker.Body) map[string]any {
	out := map[string]any{"url": u, "title": title, "size": b.Size}
	bodyFields(out, b)
	return out
}

// fetchRequest — запрос browser.get / head / post / put / patch / delete из
// аргументов: метод — имя действия, headers — JSON-объект строк.
func fetchRequest(r abi.ExecRequest) (worker.FetchRequest, error) {
	fr := worker.FetchRequest{Method: strings.ToUpper(r.Action), URL: str(r.Args, "url"), Max: num(r.Args, "max_chars")}
	if fr.URL == "" {
		return fr, fmt.Errorf("%s: url is required", r.Action)
	}
	if h := str(r.Args, "headers"); h != "" {
		if err := json.Unmarshal([]byte(h), &fr.Headers); err != nil {
			return fr, fmt.Errorf(`%s: headers must be a JSON object of strings, e.g. {"Accept":"application/json"}: %v`, r.Action, err)
		}
	}
	if b, ok := r.Args["body"].(string); ok && b != "" {
		if fr.Method == "GET" || fr.Method == "HEAD" {
			return fr, fmt.Errorf("%s has no body", r.Action)
		}
		if n := utf8.RuneCountInString(b); n > worker.MaxFetchBody {
			return fr, fmt.Errorf("argument \"body\" is too long: %d characters, the limit is %d", n, worker.MaxFetchBody)
		}
		fr.Body = &b
		if !hasHeader(fr.Headers, "content-type") && json.Valid([]byte(b)) {
			if fr.Headers == nil {
				fr.Headers = map[string]string{}
			}
			fr.Headers["Content-Type"] = "application/json"
		}
	}
	return fr, nil
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// BriefArg — до стольких символов строковый аргумент виден в кратких шагах close --steps.
const BriefArg = 200

// closeResult — ответ close: по умолчанию коротко {session, closed, steps: N};
// steps: true — шаги кратко (действие, аргументы, статус, без ответов и
// текстов страниц); save_steps: true — полная запись (фикстура
// hosttest.Replay) в файл каталога данных gate4ai, путь — steps_file.
func (as *agentSession) closeResult(a map[string]any) (abi.ExecResult, error) {
	res := map[string]any{"session": as.id, "closed": true, "steps": len(as.steps)}
	if as.truncated {
		res["truncated"] = true
	}
	if full, _ := a["steps"].(bool); full {
		res["step_list"] = briefSteps(as.steps)
	}
	if save, _ := a["save_steps"].(bool); save {
		path, err := saveSteps(as.id, as.steps)
		if err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeIO, "the session is closed, its steps were not saved: %v", err)
		}
		res["steps_file"] = path
	}
	return abi.ExecResult{Structured: res, Source: "web:session:" + as.id}, nil
}

// briefSteps — шаги без результатов: действие, аргументы (длинные строки
// обрезаны), status ok или код ошибки.
func briefSteps(steps []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(steps))
	for _, st := range steps {
		args := map[string]any{}
		if m, ok := st["args"].(map[string]any); ok {
			for k, v := range m {
				if s, ok := v.(string); ok && utf8.RuneCountInString(s) > BriefArg {
					v = string([]rune(s)[:BriefArg]) + "…"
				}
				args[k] = v
			}
		}
		b := map[string]any{"action": st["action"], "args": args, "status": "ok"}
		if e, ok := st["error"].(map[string]any); ok {
			b["status"] = e["code"]
		}
		if via, ok := st["via"]; ok {
			b["via"] = via
		}
		out = append(out, b)
	}
	return out
}

// dataDir — каталог данных gate4ai (как у daemon: GATE4AI_HOME или каталог ОС).
func dataDir() string {
	if h := os.Getenv("GATE4AI_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "gate4ai")
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "gate4ai")
		}
		return filepath.Join(home, "AppData", "Local", "gate4ai")
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "gate4ai")
	}
	return filepath.Join(home, ".local", "state", "gate4ai")
}

// saveSteps пишет шаги сеанса фикстурой ({session, steps}) в
// <каталог данных>/browser-steps/<сеанс>.json (0600).
func saveSteps(id string, steps []map[string]any) (string, error) {
	dir := filepath.Join(dataDir(), "browser-steps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"session": id, "steps": steps})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".json")
	return path, os.WriteFile(path, b, 0o600)
}
