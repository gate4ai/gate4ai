// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Трассировка и статус проверенности действий коннекторов
// (docs/connector-sharing-ux.md, «коннектор ведёт, агент страхует»): пока
// действие — черновик (меньше VerifiedAfter подтверждённых прогонов), его вызов
// возвращает агенту подвызовы браузера, а при сбое — живую страницу сеансом
// агента (handoff_to_agent), чтобы он доделал задачу и починил коннектор.

// VerifiedAfter — столько подтверждённых прогонов подряд делают действие verified.
const VerifiedAfter = 3

// Статусы действий по прогонам.
const (
	RunDraft    = "draft"
	RunVerified = "verified"
)

// CodeHandoffToAgent — действие коннектора не удалось, его страница отдана
// агенту сеансом браузера (Result.Handoff).
const CodeHandoffToAgent = "handoff_to_agent"

// TraceStep — подвызов браузера (или другого поставщика) во время действия.
type TraceStep struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args,omitempty"`
	Status string         `json:"status"` // ok | <код ошибки>
	Error  string         `json:"error,omitempty"`
	URL    string         `json:"url,omitempty"`
	Title  string         `json:"title,omitempty"`
	Text   string         `json:"text,omitempty"` // начало текста страницы
}

// HandoffToAgent — страница неудавшегося действия отдана агенту.
type HandoffToAgent struct {
	Session    string `json:"session"`
	URL        string `json:"url,omitempty"`
	ViewURL    string `json:"view_url,omitempty"`
	FailedStep int    `json:"failed_step"` // номер подвызова в trace (с 1), 0 — до первого
	Hint       string `json:"hint"`
}

// RunStats — прогоны действия: подтверждённые подряд, последний результат.
type RunStats struct {
	OK     int       `json:"verified_runs"`
	Status string    `json:"status"`
	Last   time.Time `json:"last,omitempty"`
	LastOK bool      `json:"last_ok"`
}

// TraceMax — больше подвызовов в ответ не кладётся (остальные — в журнале, по квитанции).
const TraceMax = 60

// TraceText — до стольких символов текста страницы в шаге трассы.
const TraceText = 300

type traceKey struct{}

// WithTrace — агент просил трассировку (--trace).
func WithTrace(ctx context.Context) context.Context { return context.WithValue(ctx, traceKey{}, true) }

// traceBuf — подвызовы корневого действия.
type traceBuf struct {
	mu    sync.Mutex
	steps []TraceStep
	more  int
}

func (t *traceBuf) add(s TraceStep) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.steps) >= TraceMax {
		t.more++
		return
	}
	t.steps = append(t.steps, s)
}

func (t *traceBuf) list() []TraceStep {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]TraceStep(nil), t.steps...)
	if t.more > 0 {
		out = append(out, TraceStep{Action: "…", Status: fmt.Sprintf("%d more calls in the journal (gate4ai log --parent)", t.more)})
	}
	return out
}

// traced — трассировать корневое действие: агент просил (--trace) или
// действие коннектора ещё черновик. Встроенные коннекторы не трассируются.
func (p *Pipeline) traced(ctx context.Context, m manifest.Manifest, action string) bool {
	if m.Runtime == manifest.RuntimeBuiltin {
		return false
	}
	if on, _ := ctx.Value(traceKey{}).(bool); on {
		return true
	}
	return p.ActionRuns(action).Status == RunDraft
}

// runsKey — ключ State для прогонов действия.
func runsKey(action string) string { return "runs:" + action }

// ActionRuns — прогоны действия (встроенные коннекторы — всегда verified без счёта).
func (p *Pipeline) ActionRuns(action string) RunStats {
	conn := strings.SplitN(action, ".", 2)[0]
	if c, ok := p.Registry.Get(conn); ok && c.Manifest().Runtime == manifest.RuntimeBuiltin {
		return RunStats{Status: RunVerified}
	}
	st := RunStats{Status: RunDraft}
	if p.State == nil {
		return st
	}
	if raw, ok := p.State.Get(runsKey(action)); ok {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	st.Status = RunDraft
	if st.OK >= VerifiedAfter {
		st.Status = RunVerified
	}
	return st
}

// recordRun — результат корневого прогона: удача прибавляет подтверждённый
// прогон, неудача (ошибка, not_confirmed, handoff_to_agent) обнуляет счёт.
func (p *Pipeline) recordRun(action string, ok bool) RunStats {
	st := p.ActionRuns(action)
	if ok {
		st.OK++
	} else {
		st.OK = 0
	}
	st.Last, st.LastOK = p.Now(), ok
	st.Status = RunDraft
	if st.OK >= VerifiedAfter {
		st.Status = RunVerified
	}
	if p.State != nil {
		raw, _ := json.Marshal(st)
		if err := p.State.Set(runsKey(action), string(raw)); err != nil {
			p.Log.Warn("state: runs", "action", action, "err", err)
		}
	}
	if p.RunHook != nil {
		p.RunHook(action, ok, st)
	}
	return st
}

// traceStep — шаг трассы из вложенного вызова: аргументы без секретов и без
// длинных значений, адрес и начало текста страницы из ответа.
func (p *Pipeline) traceStep(action string, act manifest.Action, args map[string]any, res Result) TraceStep {
	s := TraceStep{Action: action, Status: "ok", Args: map[string]any{}}
	masked := p.maskArgs(act, args)
	for k, v := range masked {
		if k == "session" {
			continue
		}
		if str, ok := v.(string); ok && len(str) > 200 {
			v = str[:200] + "…"
		}
		s.Args[k] = v
	}
	if res.Status != StatusOK {
		s.Status = res.Code
		if res.connectorCode != "" {
			s.Status = res.connectorCode
		}
		s.Error = res.Message
	}
	if m, ok := res.Result.(map[string]any); ok {
		s.URL, _ = m["url"].(string)
		s.Title, _ = m["title"].(string)
		if t, _ := m["text"].(string); t != "" {
			if r := []rune(t); len(r) > TraceText {
				t = string(r[:TraceText]) + "…"
			}
			s.Text = t
		}
	}
	return s
}
