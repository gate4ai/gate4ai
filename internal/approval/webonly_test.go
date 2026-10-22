// SPDX-License-Identifier: AGPL-3.0-only

package approval

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Самонастройку gate4ai в Telegram одним нажатием не одобрить: кнопки
// «Разрешить» нет, а подделанный callback "y:" отклоняется (code review E.5).
func TestTelegramSelfConfigIsWebOnly(t *testing.T) {
	d, err := daemon.New(daemon.Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(),
		InitialConfig: []byte("agents: {bot: {profile: p}}\nprofiles: {p: {connectors: []}}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fake := &fakeTelegram{token: "1:T"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	const owner = int64(7)
	tg := NewTelegram(&BotAPI{Base: srv.URL, Token: "1:T"}, owner, d.Pipeline, d.State)
	d.AddNotifier(tg)
	ctx := context.Background()
	res := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "x", "changes": []any{map[string]any{"op": "add_list_items", "list": "l", "items": []any{"a"}}}}})
	if res.Status != pipeline.StatusPending || len(fake.sent) != 1 {
		t.Fatalf("%+v %d", res, len(fake.sent))
	}
	msg := fake.sent[0]
	if !strings.Contains(msg["text"].(string), "Web UI") {
		t.Fatal(msg["text"])
	}
	kb := msg["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	for _, b := range kb {
		if data := b.(map[string]any)["callback_data"].(string); strings.HasPrefix(data, "y:") {
			t.Fatal("approve button for a self-config change")
		}
	}
	no := kb[0].(map[string]any)["callback_data"].(string)
	yes := "y" + strings.TrimPrefix(no, "n")
	tg.Handle(ctx, cbUpdate(1, owner, yes))
	if a, _ := d.State.GetApproval(res.ID); a.Status != "pending" || fake.answers[0] != WebOnlyHint {
		t.Fatalf("%s %v", a.Status, fake.answers)
	}
	// Отклонить можно и из Telegram.
	tg.Handle(ctx, cbUpdate(2, owner, no))
	if a, _ := d.State.GetApproval(res.ID); a.Status == "pending" {
		t.Fatal("reject from telegram")
	}
}
