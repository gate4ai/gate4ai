// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/state"
)

// ---- Запросы агентов на подключение (gate4ai pair) ----

// pairingView — запрос на подключение для владельца: код для сверки с чатом,
// каталог и процессы агента, права.
type pairingView struct {
	*state.Pairing
	Exists   bool     // агент уже есть: ключ добавится к нему
	Profile  string   // права: текущие у агента или по умолчанию
	Profiles []string // на выбор (новому агенту)
	Revoked  bool     // владелец уже отзывал доступ этого агента
	Same     int      // других ждущих запросов того же агента (сверить код!)
	Chain    string   // процессы: агент ← … ← gate4ai
	Session  string
}

func (u *UI) pairingViews() []pairingView {
	ps, _ := u.D.State.PendingPairings()
	cfg := u.D.Config.Current()
	profiles := slices.Sorted(maps.Keys(cfg.Profiles))
	count := map[string]int{}
	for _, p := range ps {
		count[p.Agent]++
	}
	var out []pairingView
	for _, p := range ps {
		v := pairingView{Pairing: p, Profiles: profiles, Same: count[p.Agent] - 1}
		if a, ok := cfg.Agents[p.Agent]; ok {
			v.Exists, v.Profile = true, a.Profile
		} else if a, ok := cfg.Agents[p.Kind]; ok && p.Kind != "" {
			v.Profile = a.Profile
		}
		if at, err := u.D.State.AgentRevokedAt(p.Agent); err == nil && !at.IsZero() {
			v.Revoked = true
		}
		var c audit.Caller
		if json.Unmarshal(p.Caller, &c) == nil {
			v.Session = c.Session
			var names []string
			for i := len(c.Chain) - 1; i >= 0; i-- {
				n := c.Chain[i].Name
				if n == "" {
					n = c.Chain[i].Exe
				}
				names = append(names, n)
			}
			v.Chain = strings.Join(names, " → ")
		}
		out = append(out, v)
	}
	return out
}

// decidePairing — решение по запросу на подключение: только в Web UI на этом
// компьютере (и в Telegram), не командой CLI.
func (u *UI) decidePairing(w http.ResponseWriter, r *http.Request) map[string]any {
	back := or(r.PostForm.Get("back"), "/agents")
	approve := r.PostForm.Get("decision") == "approve"
	profile := r.PostForm.Get("profile")
	if profile == "_new" {
		profile = ""
	}
	p, err := u.D.DecidePairing(r.Context(), r.PathValue("id"), approve, profile, "webui")
	switch {
	case errors.Is(err, state.ErrAlreadyDecided):
		return redirect(back, M("Already decided"))
	case errors.Is(err, state.ErrExpired):
		return redirectErr(back, M("The request expired: the agent asks again with a new code"))
	case errors.Is(err, state.ErrNoPairing):
		return redirectErr(back, M("The request was not found"))
	case err != nil:
		return redirectErr(back, err)
	case !approve:
		return redirect(back, M("Connection of %s rejected", p.Agent))
	}
	return redirect("/agents/"+p.Agent, M("%s is connected. It calls gate4.ai with its own key now; what it may do is set below.", p.Agent))
}
