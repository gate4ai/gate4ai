// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

func TestQuestions(t *testing.T) {
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), InitialConfig: []byte("agents: {}\nprofiles: { p: { connectors: [] } }\n"), Keys: keys.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Pipeline.Ask == nil || d.Pipeline.Vault == nil {
		t.Fatal("pipeline is not wired")
	}
	got := make(chan string, 1)
	go func() {
		v, err := d.Pipeline.Ask(context.Background(), pipeline.Question{Agent: "bot", Caller: "mos_login", Account: "@emias", Bind: []string{"https://login.mos.ru"}, Text: "Код из SMS"})
		if err != nil {
			v = "error: " + err.Error()
		}
		got <- v
	}()
	var qs []Question
	for i := 0; i < 100 && len(qs) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		qs = d.Questions()
	}
	if len(qs) != 1 || !strings.Contains(qs[0].Prompt(), "mos_login (bot) signs in on https://login.mos.ru with @emias and asks: Код из SMS") {
		t.Fatalf("%+v", qs)
	}
	if err := d.Answer("q_nope", "1"); err == nil {
		t.Fatal("unknown question")
	}
	if err := d.Answer(qs[0].ID, "246813\n"); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != "246813" {
		t.Fatal(v)
	}
	// Ответ маскируется в ответах, вопрос снят.
	if d.Vault.MaskString("code 246813") != "code ***" || len(d.Questions()) != 0 {
		t.Fatal(d.Vault.MaskString("code 246813"), d.Questions())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := d.Pipeline.Ask(ctx, pipeline.Question{Agent: "bot"}); err == nil {
		t.Fatal("no answer in time")
	}
}
