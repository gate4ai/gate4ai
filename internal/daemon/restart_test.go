// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Коннектор, на аргументы которого ссылается политика, удалён: daemon всё
// равно стартует (H11 / E.1), предупреждение видно владельцу, новые ошибки
// в политике по-прежнему отклоняются.
func TestRestartWithoutConnector(t *testing.T) {
	home := filepath.Join(t.TempDir(), "data")
	ks := keys.NewMemory()
	n1, _ := connectors.NewInProcess(&notes{})
	d, err := New(Options{Home: home, InitialConfig: []byte(cfg), Keys: ks, Connectors: []connectors.Client{n1}})
	if err != nil {
		t.Fatal(err)
	}
	if w := d.PolicyWarning(); w != "" {
		t.Fatal("unexpected warning:", w)
	}
	d.Close()

	// «Перезапуск» без коннектора notes.
	d, err = New(Options{Home: home, Keys: ks})
	if err != nil {
		t.Fatalf("daemon must start when a connector of the policy is missing: %v", err)
	}
	if w := d.PolicyWarning(); !strings.Contains(w, "text") {
		t.Fatalf("warning: %q", w)
	}
	owner := auth.OwnerVia("socket")
	// Несвязанная правка проходит (старая ошибка терпится).
	if _, err := d.Config.Edit(owner, "agent", func(c *config.Config) error { return c.SetAgent("bot2", "p", "") }); err != nil {
		t.Fatalf("unrelated edit: %v", err)
	}
	// Новая ошибка — нет.
	raw, _ := d.Config.CurrentRaw()
	bad := strings.Replace(string(raw), "rules:", "rules:\n      - { id: new-bad, action: notes.read, when: \"foo == 1\", effect: deny }", 1)
	if _, err := d.Config.Put(owner, []byte(bad), "bad"); err == nil || !strings.Contains(err.Error(), "foo") {
		t.Fatalf("new error must be rejected: %v", err)
	}

	// Сокеты: argv и предупреждение — только владельцу.
	dir, _ := os.MkdirTemp("", "g4ar")
	defer os.RemoveAll(dir)
	ownerSock, agentSock := filepath.Join(dir, "o.sock"), filepath.Join(dir, "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, ownerSock, agentSock) }()
	for i := 0; i < 200 && !ipc.Alive(agentSock); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	var h map[string]any
	client{c: ipc.HTTPClient(ownerSock)}.do(t, "GET", "/v1/health", nil, &h)
	if h["args"] == nil || h["policy_warning"] == nil {
		t.Fatal("owner health:", h)
	}
	h = nil
	client{c: ipc.HTTPClient(agentSock)}.do(t, "GET", "/v1/health", nil, &h)
	if h["ok"] != true || h["args"] != nil || h["policy_warning"] != nil {
		t.Fatal("agent socket health must not show argv:", h)
	}
	// PUT /v1/policy по старой версии — конфликт, а не откат чужой правки.
	raw, n := d.Config.CurrentRaw()
	o := client{c: ipc.HTTPClient(ownerSock)}
	if code := o.do(t, "PUT", "/v1/policy", map[string]any{"yaml": string(raw), "base": n - 1}, nil); code != 409 {
		t.Fatal("stale base:", code)
	}
	if code := o.do(t, "PUT", "/v1/policy", map[string]any{"yaml": string(raw), "base": n}, nil); code != 200 {
		t.Fatal("current base:", code)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	d.Close()

	// Коннектор вернулся — предупреждения нет.
	n1, _ = connectors.NewInProcess(&notes{})
	d, err = New(Options{Home: home, Keys: ks, Connectors: []connectors.Client{n1}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if w := d.PolicyWarning(); w != "" {
		t.Fatal("warning after the connector is back:", w)
	}
}

// --reset-ui-login не повторяется при restart (E.14).
func TestFilterOneShot(t *testing.T) {
	got := filterOneShot([]string{"--home", "/x", "--reset-ui-login", "-reset-ui-login=true", "--ui", "127.0.0.1:1"})
	if !slices.Equal(got, []string{"--home", "/x", "--ui", "127.0.0.1:1"}) {
		t.Fatal(got)
	}
}
