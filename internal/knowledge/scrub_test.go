// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
)

func TestScrubberPersonalKeys(t *testing.T) {
	doc := map[string]any{
		"result": map[string]any{"json": map[string]any{"alias": "ivan_petrov", "email": "ivan@mail.ru", "phone": "+7 916 123-45-67", "id": 1234567890.0, "karma": 12.0},
			"url": "https://habr.com/kek/v2/users/ivan_petrov/card"},
		"text": "Привет, ivan_petrov! Почта ivan@mail.ru",
	}
	s := NewScrubber(nil, true)
	out := s.Walk(doc).(map[string]any)
	j := out["result"].(map[string]any)["json"].(map[string]any)
	if j["alias"] != "alias_1" || !strings.HasPrefix(j["email"].(string), "email_") || j["karma"] != 12.0 || j["id"] == 1234567890.0 || j["id"].(float64) < 1 {
		t.Fatalf("%+v", j)
	}
	if u := out["result"].(map[string]any)["url"].(string); strings.Contains(u, "ivan_petrov") || !strings.Contains(u, "alias_1") {
		t.Fatal("alias in the url (seen before the key):", u)
	}
	if txt := out["text"].(string); strings.Contains(txt, "ivan") || strings.Contains(txt, "mail.ru") {
		t.Fatal(txt)
	}
	if !s.Seen()["ivan_petrov"] || s.Count == 0 {
		t.Fatalf("%v %d", s.Seen(), s.Count)
	}
	// Без auto — только пары.
	s2 := NewScrubber(map[string]string{"Иванов": "Петров"}, false)
	if got := s2.Walk("Иванов, ivan@mail.ru"); got != "Петров, ivan@mail.ru" {
		t.Fatal(got)
	}
}

func TestAutoRecipeAndNote(t *testing.T) {
	dir := t.TempDir() + "/knowledge" // папки знаний ещё нет
	steps := []hosttest.Step{
		{Action: "open", Args: map[string]any{"url": "https://account.habr.com/login/"}, Result: map[string]any{"url": "https://account.habr.com/login/", "title": "Вход", "login_required": map[string]any{}}},
		{Action: "act", Args: map[string]any{"selector": "button", "text": "Войти", "method": "click"}, Error: &abi.Error{Code: "not_found", Message: "no button"}},
		{Action: "get", Args: map[string]any{"url": "https://habr.com/kek/v2/me"}, Result: map[string]any{"status": 200.0, "body": "<html>…"}},
		{Action: "close"},
	}
	site, domains := SessionSite(steps)
	if site != "account.habr.com" || len(domains) != 2 {
		t.Fatal(site, domains)
	}
	now := time.Date(2026, 10, 27, 12, 30, 0, 0, time.UTC)
	res, ok, err := AutoRecipe(dir, SessionRecord{ID: "s-1", Agent: "claude-code", At: now, Steps: steps})
	if err != nil || !ok || !res.Created || res.Folder != dir+"/gate4ai-habr" || res.Status != StatusAuto || res.Sessions != 1 {
		t.Fatalf("%+v %v %v", res, ok, err)
	}
	f := Read(res.Folder)
	if f.Recipe == nil || f.Recipe.Site != "account.habr.com" || f.Recipe.Domains[0] != "habr.com" || !f.MatchesSite("habr.com") || f.Recipe.Sessions[0].At != "2026-10-27 12:30" {
		t.Fatalf("%+v", f.Recipe)
	}
	// Второй сеанс на другом поддомене того же сайта — та же папка.
	res2, _, err := AutoRecipe(dir, SessionRecord{ID: "s-2", Agent: "codex", At: now.Add(time.Hour), Steps: steps[:1]})
	if err != nil || res2.Created || res2.Folder != res.Folder || res2.Sessions != 2 {
		t.Fatalf("%+v %v", res2, err)
	}
	// Заметка: последнему сеансу автора без заметки; владельцу — последнему.
	if _, err := AddNote(res.Recipe, "claude-code", "two-step sign-in", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, err := AddNote(res.Recipe, "", "owner: use the API", now.Add(3*time.Hour))
	if err != nil || s.Session != "s-2" {
		t.Fatal(s, err)
	}
	r := Read(res.Folder).Recipe
	if r.Sessions[0].Note != "two-step sign-in" || r.Sessions[1].Note != "owner: use the API" || r.WithoutNote() != 0 || r.LastSession() != "2026-10-27 13:30" || r.Overall() != StatusAuto {
		t.Fatalf("%+v", r.Sessions)
	}
	if _, err := AddNote(res.Recipe, "x", "  ", now); err == nil {
		t.Fatal("empty note accepted")
	}
	// Только запросы к API, без страниц — сайт по первому запросу.
	if site, _ := SessionSite(steps[2:]); site != "habr.com" {
		t.Fatal(site)
	}
	if _, ok, _ := AutoRecipe(dir, SessionRecord{Steps: steps[3:]}); ok {
		t.Fatal("recipe from a session without pages")
	}
}
