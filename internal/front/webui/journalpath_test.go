// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// pager — коннектор с действиями на странице: open принимает адрес, act его
// не принимает и сообщает адрес страницы после действия (ExecResult.Page).
type pager struct{}

func (pager) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "web", Actions: []manifest.Action{
		{Name: "open", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { url: { type: string } }, required: [url] }`)},
		{Name: "act", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { selector: { type: string } } }`)},
		{Name: "status", Class: "read", InputSchema: manifest.MustSchema(`{ type: object }`)},
	}}
}
func (pager) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	if r.Action == "act" {
		return abi.ExecResult{Structured: "ok", Page: "https://a.example.com/pay?order=7"}, nil
	}
	return abi.ExecResult{Structured: "ok"}, nil
}
func (pager) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

// Столбец «Путь» журнала: путь к файлу (с ~) для файлов, полный адрес страницы
// для браузера — из аргументов или из адреса, записанного после действия.
func TestJournalPathColumn(t *testing.T) {
	home, _ := os.UserHomeDir()
	short, full := entryPath(audit.Receipt{Args: map[string]any{"path": home + "/notes/todo.md"}})
	if short != "~/notes/todo.md" || full != home+"/notes/todo.md" {
		t.Fatal(short, full)
	}
	if p, _ := entryPath(audit.Receipt{Args: map[string]any{"url": "https://a.example.com/x?q=1"}}); p != "https://a.example.com/x?q=1" {
		t.Fatal("the full address, not the host:", p)
	}
	if p, _ := entryPath(audit.Receipt{Args: map[string]any{"selector": "#pay"}, Execution: audit.Execution{Page: "https://a.example.com/pay"}}); p != "https://a.example.com/pay" {
		t.Fatal("the page address recorded after the action:", p)
	}
	if p, _ := entryPath(audit.Receipt{Args: map[string]any{"text": "hi"}}); p != "" {
		t.Fatal("no path:", p)
	}

	u, d, _, base := setupCfg(t, "agents:\n  bot: {profile: p}\nprofiles:\n  p:\n    connectors: [notes, web]\n", pager{})
	_, _, _, get := loginAs(t, u, base)
	for _, c := range []pipeline.Call{
		{Agent: "bot", Action: "web.open", Args: map[string]any{"url": "https://a.example.com/x?q=1"}},
		{Agent: "bot", Action: "web.act", Args: map[string]any{"selector": "#pay"}},
		{Agent: "bot", Action: "web.status", Args: map[string]any{}},
	} {
		if res := d.Pipeline.Execute(context.Background(), c); res.Status != pipeline.StatusOK {
			t.Fatal(c.Action, res.Message)
		}
	}
	p := get("/journal")
	if !strings.Contains(p, "<th>Путь</th>") || !strings.Contains(p, `<td class="path"><code title="https://a.example.com/x?q=1">https://a.example.com/x?q=1</code></td>`) ||
		!strings.Contains(p, `<code title="https://a.example.com/pay?order=7">https://a.example.com/pay?order=7</code>`) {
		t.Fatal("path column:", p)
	}
	// Действие без цели — пустая ячейка; цель не дублируется в столбце «Действие».
	if !strings.Contains(p, `<td class="path"></td>`) || strings.Contains(p, "· <code>a.example.com") {
		t.Fatal("empty cell and no duplicate target:", p)
	}
}
