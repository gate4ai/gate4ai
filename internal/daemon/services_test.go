// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Раздел browser (настройки браузерной службы ядра до v0.6) переезжает в
// connectors.browser новой версией конфигурации; значения владельца там важнее.
func TestBrowserSectionMoved(t *testing.T) {
	cfg := `# my policy
agents:
  bot: { profile: p }
profiles:
  p: { connectors: [] }
connectors:
  browser: { allowed_domains: ["*.example.com"], model: mine }
browser:
  engine: cdp
  model: openai/gpt-5.4-mini
  model_key: "@llm.key"
  max_text_chars: 5000
`
	home := filepath.Join(t.TempDir(), "d")
	d, err := New(Options{Home: home, InitialConfig: []byte(cfg), Keys: keys.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	c := d.Config.Current()
	b := c.Connectors["browser"]
	if b["engine"] != "cdp" || b["model"] != "mine" || b["model_key"] != "@llm.key" || b["allowed_domains"] == nil || b["max_text_chars"] == nil {
		t.Fatal(b)
	}
	raw, n := d.Config.CurrentRaw()
	if n != 2 || !strings.HasPrefix(string(raw), "# my policy") || strings.Contains(string(raw), "\nbrowser:") {
		t.Fatalf("version %d:\n%s", n, raw)
	}
	// Повторный запуск ничего не меняет.
	d.Close()
	d, err = New(Options{Home: home, Keys: keys.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, n := d.Config.CurrentRaw(); n != 2 {
		t.Fatal("migrated twice:", n)
	}
}
