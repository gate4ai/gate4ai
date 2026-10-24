// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"time"
)

// Changes — что изменилось на странице за действие type / press: появились и
// пропали элементы (тег в поле тегов, вариант подсказки), открылись и
// закрылись всплывающие списки, меню и диалоги, сменилось aria-expanded.
type Changes struct {
	Added     []Changed `json:"added,omitempty"`
	Removed   []Changed `json:"removed,omitempty"`
	Opened    []Changed `json:"opened,omitempty"`
	Closed    []Changed `json:"closed,omitempty"`
	Expanded  []Changed `json:"expanded,omitempty"`  // aria-expanded стал true
	Collapsed []Changed `json:"collapsed,omitempty"` // aria-expanded стал false
	More      int       `json:"more,omitempty"`      // сколько изменений не показано (предел MaxChanges в каждом списке)
}

// Changed — элемент в Changes: тег, роль и видимый текст (до 200 символов).
type Changed struct {
	Tag  string `json:"tag"`
	Role string `json:"role,omitempty"`
	Text string `json:"text,omitempty"`
}

// MaxChanges — столько элементов в каждом списке Changes.
const MaxChanges = 10

// После действия ждётся, пока страница не меняется ChangeQuiet (обработчики
// ввода, подсказки с сервера), но не дольше ChangeSettle.
var (
	ChangeQuiet  = 250 * time.Millisecond
	ChangeSettle = 2 * time.Second
)

// watchJS — g4watch: перед действием запоминает видимый текст элементов рядом
// с полем в фокусе (поддерево его предка тремя уровнями выше) и во
// всплывающих списках, видимость всплывающих и aria-expanded, и включает
// MutationObserver. Текст удалённых элементов берётся только из этого
// снимка: у отсоединённого узла видимого текста нет, а textContent отдал бы
// и то, что сайт прятал стилями.
var watchJS = `(() => {
  const vt = ` + visibleTextJS + `;
  if (window.__g4watch) window.__g4watch.obs.disconnect();
  const pop = '[role=listbox],[role=menu],[role=dialog],[role=alertdialog],[role=tooltip],dialog,[aria-modal=true]';
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none'; };
  const st = {added: [], removed: [], last: Date.now(), text: new Map(), popups: new Map(), expanded: new Map()};
  let root = document.activeElement && document.activeElement !== document.body ? document.activeElement : document.body;
  for (let i = 0; i < 3 && root.parentElement && root !== document.body; i++) root = root.parentElement;
  const near = [...root.querySelectorAll('*')].slice(0, 300);
  document.querySelectorAll(pop).forEach(p => { st.popups.set(p, vis(p)); near.push(p, ...[...p.querySelectorAll('*')].slice(0, 100)); });
  for (const e of near) st.text.set(e, vt(e).replace(/\s+/g, ' ').trim().slice(0, 200));
  document.querySelectorAll('[aria-expanded]').forEach(e => st.expanded.set(e, e.getAttribute('aria-expanded')));
  st.obs = new MutationObserver(ms => {
    st.last = Date.now();
    for (const m of ms) {
      m.addedNodes.forEach(n => { if (n.nodeType === 1) st.added.push(n); });
      m.removedNodes.forEach(n => { if (n.nodeType === 1) st.removed.push(n); });
    }
  });
  st.obs.observe(document.documentElement, {childList: true, subtree: true, attributes: true});
  window.__g4watch = st;
  return true;
})()`

// quietJS — сколько миллисекунд страница не менялась (-1 — наблюдения нет).
const quietJS = `(window.__g4watch ? Date.now() - window.__g4watch.last : -1)`

// touchJS — отсчёт тишины — с конца действия, а не с начала наблюдения.
const touchJS = `(window.__g4watch ? (window.__g4watch.last = Date.now(), true) : false)`

// changesJS — итог наблюдения watchJS (null — наблюдения нет: страница
// сменилась, или на ней введён секрет). Пара «удалён и добавлен» с тем же
// текстом — перерисовка, а не изменение.
var changesJS = `((secret, max) => {
  const st = window.__g4watch;
  if (!st) return null;
  st.obs.disconnect();
  delete window.__g4watch;
  if (secret) return null;
  const vt = ` + visibleTextJS + `;
  const pop = '[role=listbox],[role=menu],[role=dialog],[role=alertdialog],[role=tooltip],dialog,[aria-modal=true]';
  const vis = e => { const r = e.getBoundingClientRect(), s = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  const desc = (e, text) => { const d = {tag: e.localName}; const r = e.getAttribute('role'); if (r) d.role = r; if (text) d.text = text; return d; };
  const top = set => [...set].filter(n => { for (let a = n.parentElement; a; a = a.parentElement) if (set.has(a)) return false; return true; });
  const out = {added: [], removed: [], opened: [], closed: [], expanded: [], collapsed: []};
  const added = new Set(st.added.filter(n => n.isConnected && !n.hasAttribute('data-g4-cover')));
  const removed = new Set(st.removed.filter(n => !n.isConnected && !st.added.includes(n)));
  for (const n of top(added)) {
    if (!vis(n)) continue;
    const t = vt(n).replace(/\s+/g, ' ').trim().slice(0, 200);
    if (!t && !n.getAttribute('role')) continue;
    (n.matches(pop) ? out.opened : out.added).push(desc(n, t));
  }
  for (const n of top(removed)) {
    const t = st.text.get(n) || '';
    if (st.popups.has(n)) { if (st.popups.get(n)) out.closed.push(desc(n, t)); continue; }
    if (!t && !n.getAttribute('role')) continue;
    const i = out.added.findIndex(a => a.text === t && a.tag === n.localName);
    if (i >= 0 && t) { out.added.splice(i, 1); continue; }
    out.removed.push(desc(n, t));
  }
  for (const [p, was] of st.popups) {
    if (!p.isConnected) continue;
    const now = vis(p);
    if (was && !now) out.closed.push(desc(p, st.text.get(p)));
    if (!was && now) out.opened.push(desc(p, vt(p).replace(/\s+/g, ' ').trim().slice(0, 200)));
  }
  document.querySelectorAll(pop).forEach(p => { if (!st.popups.has(p) && !added.has(p) && vis(p) && ![...added].some(a => a.contains(p))) out.opened.push(desc(p, vt(p).replace(/\s+/g, ' ').trim().slice(0, 200))); });
  document.querySelectorAll('[aria-expanded]').forEach(e => {
    const was = st.expanded.get(e), now = e.getAttribute('aria-expanded');
    if (was === now) return;
    const label = (e.getAttribute('aria-label') || vt(e) || e.placeholder || '').replace(/\s+/g, ' ').trim().slice(0, 200);
    if (now === 'true') out.expanded.push(desc(e, label)); else if (was === 'true') out.collapsed.push(desc(e, label));
  });
  let more = 0;
  const res = {};
  for (const k of Object.keys(out)) {
    if (!out[k].length) continue;
    more += Math.max(0, out[k].length - max);
    res[k] = out[k].slice(0, max);
  }
  if (more) res.more = more;
  return res;
})`

// watch начинает наблюдение за изменениями страницы (до действия).
func (x *Executor) watch(ctx context.Context) {
	var ok bool
	_ = x.Engine.Eval(ctx, watchJS, &ok)
}

// changes ждёт, пока страница затихнет (ChangeQuiet, не дольше ChangeSettle),
// и возвращает, что изменилось после watch; nil — ничего или наблюдения нет.
func (x *Executor) changes(ctx context.Context, secret bool) *Changes {
	var touched bool
	_ = x.Engine.Eval(ctx, touchJS, &touched)
	deadline := time.Now().Add(ChangeSettle)
	for time.Now().Before(deadline) {
		var quiet float64
		if err := x.Engine.Eval(ctx, quietJS, &quiet); err != nil || quiet < 0 || quiet >= float64(ChangeQuiet.Milliseconds()) {
			break
		}
		if wait(ctx, ActPoll/4) != nil {
			return nil
		}
	}
	var c *Changes
	if err := x.Engine.Eval(ctx, call(changesJS, secret || x.SecretOnPage(ctx), MaxChanges), &c); err != nil || c == nil {
		return nil
	}
	if len(c.Added)+len(c.Removed)+len(c.Opened)+len(c.Closed)+len(c.Expanded)+len(c.Collapsed) == 0 {
		return nil
	}
	return c
}
