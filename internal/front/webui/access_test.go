// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// shopConn — коннектор магазина со своими сайтами и папками (как WASM).
type shopConn struct{}

func (shopConn) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "shop", Runtime: manifest.RuntimeWasm,
		Sites:   []access.Site{{Domain: "shop.example", Access: access.Allow}, {Domain: "closed.example", Access: access.Ask}},
		Folders: []access.Folder{{Path: "~/shop", Access: access.Read}},
		Actions: []manifest.Action{{Name: "look", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: {} }`)}}}
}
func (shopConn) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{}, nil
}
func (shopConn) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

// Страницы Браузера и Файлов: списки доступа правятся строками; магазин их не показывает.
func TestAccessPages(t *testing.T) {
	u, d, _, base := setupCfg(t, "access:\n  sites: [{ domain: \"*\", access: ask }]\n  folders: [{ path: \"~/Documents\", access: ask }]\nprofiles:\n  p:\n    connectors: [notes, browser, files]\n")
	_, _, post, get := loginAs(t, u, base)
	p := get("/access/browser")
	if !strings.Contains(p, "<code>*</code>") || !strings.Contains(p, `<option value="ask" selected>`) || !strings.Contains(p, `action="/access/sites"`) {
		t.Fatal("browser page", p)
	}
	post("/access/sites", url.Values{"value": {"https://App.Example.org/"}, "access": {"allow"}})
	post("/access/sites", url.Values{"value": {"evil.example"}, "access": {"deny"}})
	if s, ok := access.MatchSite(d.Config.Current().Access.Sites, "https://app.example.org/x"); !ok || s.Access != access.Allow {
		t.Fatal(d.Config.Current().Access.Sites)
	}
	// Режима read у сайтов нет: ни в выпадающем списке, ни в подсказке; форма его не принимает.
	if p := get("/access/browser"); strings.Contains(p, `<option value="read"`) || strings.Contains(p, "чтение — чтение без вопроса") {
		t.Fatal("read mode offered for sites", p)
	}
	post("/access/sites", url.Values{"value": {"ro.example"}, "access": {"read"}})
	if s, _ := access.MatchSite(d.Config.Current().Access.Sites, "https://ro.example/"); s.Domain != "*" {
		t.Fatal("read accepted for a site", d.Config.Current().Access.Sites)
	}
	post("/access/sites", url.Values{"value": {"evil.example"}, "op": {"remove"}})
	if len(d.Config.Current().Access.Sites) != 2 {
		t.Fatal(d.Config.Current().Access.Sites)
	}
	if p := get("/access/browser"); !strings.Contains(p, "<code>app.example.org</code>") {
		t.Fatal(p)
	}
	post("/access/folders", url.Values{"value": {"~/notes"}, "access": {"append"}})
	if f, ok := access.MatchFolder(d.Config.Current().Access.Folders, "~/notes/a.md", d.Pipeline.Home); !ok || f.Access != access.Append {
		t.Fatal(d.Config.Current().Access.Folders)
	}
	if p := get("/access/files"); !strings.Contains(p, "<code>~/notes</code>") || !strings.Contains(p, "Скрытые файлы") {
		t.Fatal("files page", p)
	}
	// Неверные строки — ошибка, список не меняется.
	post("/access/folders", url.Values{"value": {"relative/x"}, "access": {"read"}})
	post("/access/sites", url.Values{"value": {"bad host"}, "access": {"allow"}})
	if len(d.Config.Current().Access.Folders) != 2 || len(d.Config.Current().Access.Sites) != 2 {
		t.Fatal(d.Config.Current().Access)
	}
	// Браузер и Файлы — не в магазине; /connectors/browser ведёт на /access/browser.
	if p := get("/connectors"); strings.Contains(p, `/connectors/browser"`) || strings.Contains(p, `/connectors/files"`) {
		t.Fatal("builtins listed as store connectors", p)
	}
	if p := get("/connectors/files"); !strings.Contains(p, `action="/access/folders"`) {
		t.Fatal("redirect to /access/files", p)
	}
}

// Страница коннектора: свои сайты и папки с предупреждением о закрытых в Браузере.
func TestConnectorAccessRows(t *testing.T) {
	u, d, _, base := setupCfg(t, "access:\n  sites: [{ domain: shop.example, access: connector }, { domain: closed.example, access: deny }]\nprofiles:\n  p:\n    connectors: [shop]\n", shopConn{})
	_, _, post, get := loginAs(t, u, base)
	p := get("/connectors/shop")
	if !strings.Contains(p, "<code>shop.example</code>") || !strings.Contains(p, "закрыто в Браузере") || !strings.Contains(p, "<code>~/shop</code>") {
		t.Fatal("connector rows", p)
	}
	post("/connectors/shop/sites", url.Values{"value": {"more.example"}, "access": {"ask"}})
	post("/connectors/shop/sites", url.Values{"value": {"closed.example"}, "op": {"remove"}})
	cs := d.Config.Current().ConnectorSites("shop")
	if len(cs) != 2 || cs[0].Domain != "shop.example" || cs[1].Domain != "more.example" || cs[1].Access != access.Ask {
		t.Fatal(cs)
	}
	post("/connectors/shop/sites", url.Values{"value": {"x.example"}, "access": {"connector"}})
	if len(d.Config.Current().ConnectorSites("shop")) != 2 {
		t.Fatal("connector mode accepted for a connector")
	}
	post("/connectors/shop/folders", url.Values{"value": {"~/shop"}, "access": {"allow"}})
	if cf := d.Config.Current().ConnectorFolders("shop"); len(cf) != 1 || cf[0].Access != access.Allow {
		t.Fatal(cf)
	}
}
