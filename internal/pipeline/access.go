// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// FilesConnector — встроенный компонент «Файлы».
const FilesConnector = "files"

// RuleAccess — идентификатор «правила» в решении по спискам доступа.
const RuleAccess = "access"

// URLResolver — браузер: адрес открытой страницы сеанса агента (session) или
// страницы корневого вызова коннектора (call); "" — страницы нет.
type URLResolver interface {
	PageURL(session, call string) string
}

// accessDecision — решение по спискам доступа: домен или папка.
type accessDecision struct {
	effect, scope, reason string
}

// accessOf — решение первого уровня для действия: Браузер — по домену адреса
// (аргумент url или страница сеанса; класс действия не важен — у сайта доступ
// либо есть, либо нет), Файлы — по папке пути и операции. ok=false —
// действие не касается сайта или папки (browser.close, другие коннекторы):
// решает только политика. nested — вызов из WASM-коннектора (service_call):
// действуют и строки самого коннектора.
func (p *Pipeline) accessOf(action string, act manifest.Action, args map[string]any, cli connectors.Client, nested *nestedCall, root string) (accessDecision, bool) {
	conn := strings.SplitN(action, ".", 2)[0]
	cfg := p.cfg()
	switch conn {
	case BrowserConnector:
		u, _ := args["url"].(string)
		if u == "" {
			session, _ := args["session"].(string)
			if r, ok := cli.(URLResolver); ok && (session != "" || nested != nil) {
				u = r.PageURL(session, root)
			}
		}
		if u == "" || domain.HostOf(u) == "" {
			return accessDecision{}, false
		}
		host := domain.HostOf(u)
		g, ok := access.MatchSite(cfg.Access.Sites, u)
		if !ok {
			return accessDecision{effect: access.Deny, scope: "site:" + host, reason: fmt.Sprintf("site %s is not in the Browser access list", host)}, true
		}
		if nested == nil {
			eff := access.Effect(g.Access, false)
			return accessDecision{effect: eff, scope: "site:" + host, reason: siteReason(g)}, true
		}
		cs, ok := access.MatchSite(nested.sites, u)
		if !ok {
			return accessDecision{effect: access.Deny, scope: "site:" + host, reason: fmt.Sprintf("connector %s does not list site %s", nested.caller, host)}, true
		}
		eff := stricter(access.Effect(cs.Access, false), access.Effect(g.Access, true))
		reason := fmt.Sprintf("site %s: %s for connector %s", cs.Domain, cs.Access, nested.caller)
		if eff == access.Deny && g.Access == access.Deny {
			reason = fmt.Sprintf("site %s is denied in the Browser access list", g.Domain)
		}
		return accessDecision{effect: eff, scope: "site:" + host, reason: reason}, true
	case FilesConnector:
		path, _ := args["path"].(string)
		if path == "" {
			return accessDecision{}, false
		}
		op := access.OpOf(action)
		f, ok := access.MatchFolder(cfg.Access.Folders, path, p.Home)
		dir := folderOf(path, op, p.Home)
		if !ok {
			return accessDecision{effect: access.Deny, scope: "folder:" + dir, reason: fmt.Sprintf("path %s is not in the Files access list", path)}, true
		}
		eff := access.FolderEffect(f.Access, op, nested != nil)
		return accessDecision{effect: eff, scope: "folder:" + dir, reason: fmt.Sprintf("folder %s: %s (%s)", f.Path, f.Access, op)}, true
	}
	return accessDecision{}, false
}

func siteReason(s access.Site) string {
	if s.Access == access.Connector {
		return fmt.Sprintf("site %s is available only through a connector, not to agents directly", s.Domain)
	}
	return fmt.Sprintf("site %s: %s", s.Domain, s.Access)
}

// folderOf — папка, которую предложит подтверждение («добавить в разрешённые»):
// для файла — его каталог, для списка — сам путь.
func folderOf(path, op, home string) string {
	e := access.Expand(path, home)
	if op == access.OpList {
		return e
	}
	return filepath.Dir(e)
}

// stricter — строже из двух решений (deny > ask > allow).
func stricter(a, b string) string {
	rank := map[string]int{access.Allow: 0, access.Ask: 1, access.Deny: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// mergeAccess складывает решение политики с решением по спискам доступа:
// deny списка — всегда deny; без сработавшего правила решает список (вместо
// умолчания класса); явное правило побеждает список, но домен или папка
// остаются в решении для кнопок подтверждения.
func mergeAccess(dec policy.Decision, acc accessDecision, ok bool) policy.Decision {
	if !ok {
		return dec
	}
	switch {
	case acc.effect == access.Deny:
		return policy.Decision{Effect: config.EffectDeny, RuleIDs: []string{RuleAccess}, Reason: acc.reason, Scope: acc.scope}
	case dec.Default:
		return policy.Decision{Effect: acc.effect, RuleIDs: []string{RuleAccess}, Reason: acc.reason, Scope: acc.scope}
	}
	dec.Scope = acc.scope
	return dec
}

// settingsFor — настройки компонента для вызова: встроенные Браузер и Файлы
// получают из списков доступа, куда они вообще могут обратиться (всё, кроме
// deny): сайты — allowed_domains / blocked / mode, папки — roots. Решение
// allow / ask / deny по строке принимает pipeline до вызова.
func (p *Pipeline) settingsFor(conn string) map[string]any {
	cfg := p.cfg()
	base := cfg.Connectors[conn]
	switch conn {
	case BrowserConnector:
		out := make(map[string]any, len(base)+3)
		for k, v := range base {
			out[k] = v
		}
		domains, blocked, blocklist := access.OpenSites(cfg.Access.Sites)
		out["allowed_domains"], out["blocked"] = anyStrings(domains), anyStrings(blocked)
		out["mode"] = "allowlist"
		if blocklist {
			out["mode"] = "blocklist"
		}
		return out
	case FilesConnector:
		out := make(map[string]any, len(base)+1)
		for k, v := range base {
			out[k] = v
		}
		out["roots"] = anyStrings(access.OpenFolders(cfg.Access.Folders))
		return out
	}
	return base
}

// ConnectorSites — действующие домены WASM-коннектора: настройки владельца или
// умолчания манифеста (с подставленными настройками).
func (p *Pipeline) ConnectorSites(conn string) []access.Site {
	cfg := p.cfg()
	if s := cfg.ConnectorSites(conn); s != nil {
		return s
	}
	if cli, ok := p.Registry.Get(conn); ok {
		m := cli.Manifest()
		return m.ResolveSites(cfg.Connectors[conn])
	}
	return nil
}

// ConnectorFolders — действующие папки WASM-коннектора (см. ConnectorSites).
func (p *Pipeline) ConnectorFolders(conn string) []access.Folder {
	cfg := p.cfg()
	if f := cfg.ConnectorFolders(conn); f != nil {
		return f
	}
	if cli, ok := p.Registry.Get(conn); ok {
		m := cli.Manifest()
		return m.ResolveFolders(cfg.Connectors[conn])
	}
	return nil
}

// browserParams — сайты вызывающего коннектора для страницы браузера: его
// открытые домены и домены, закрытые в Браузере (blocked).
func (p *Pipeline) browserParams(sites []access.Site) map[string]any {
	domains, blocked, blocklist := access.OpenSites(sites)
	_, gblocked, _ := access.OpenSites(p.cfg().Access.Sites)
	mode := "allowlist"
	if blocklist {
		mode = "blocklist"
	}
	return map[string]any{"domains": anyStrings(domains), "blocked": anyStrings(append(blocked, gblocked...)), "mode": mode}
}

func anyStrings(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

// GlobalFolders — список папок Файлов для функций хоста fs_* коннекторов магазина.
func (p *Pipeline) GlobalFolders() []access.Folder { return p.cfg().Access.Folders }

// client — коннектор действия (nil — нет).
func (p *Pipeline) client(action string) connectors.Client {
	cli, _, ok := p.Registry.Lookup(action)
	if !ok {
		return nil
	}
	return cli
}

// nestedWait — сколько вложенный вызов ждёт подтверждения человека (строка
// коннектора ask): остаток срока вызывающего действия, не больше таймаута подтверждений.
func (p *Pipeline) nestedWait(ctx context.Context) time.Duration {
	d := p.cfg().Approvals.Timeout.D()
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		d = time.Until(dl)
	}
	return d
}
