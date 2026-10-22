// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Element — интерактивный элемент страницы для act: CSS-селектор, по которому
// он находится однозначно, и то, что видит человек.
type Element struct {
	Selector string `json:"selector"`
	Kind     string `json:"kind"` // field | button | link
	Tag      string `json:"tag"`
	Type     string `json:"type,omitempty"`
	Role     string `json:"role,omitempty"`
	Text     string `json:"text,omitempty"`
	Href     string `json:"href,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Value — начало значения поля (ElementValue символов), ValueLength — его
	// длина; у пароля и на странице с введённым секретом их нет.
	Value       string `json:"value,omitempty"`
	ValueLength int    `json:"value_length,omitempty"`
	Checked     bool   `json:"checked,omitempty"`
}

// MaxElements — предел списка элементов в observe.
const MaxElements = 300

// ElementValue — сколько символов значения поля показывает observe.
const ElementValue = 200

// Виды элементов observe (--kinds).
var ElementKinds = []string{"fields", "buttons", "links"}

// adHosts — рекламные и трекинговые адреса: ссылки и iframe с ними observe
// не показывает (длинные адреса adfox на килобайт — шум для агента).
const adHosts = `adfox|doubleclick|googlesyndication|googleadservices|adservice|adriver|criteo|taboola|outbrain|` +
	`an\\.yandex|yabs\\.yandex|mc\\.yandex|adnxs|adsrvr|amazon-adsystem|moatads|scorecardresearch|smartadserver|` +
	`mytarget|top-fwz1\\.mail|ad\\.mail\\.ru|[/.]ads\\.|/ads/|[?&](yclid|gclid|fbclid)=`

// elementsJS — видимые интерактивные элементы: поля, кнопки и ссылки (kinds —
// какие; пусто — все), кроме рекламных и трекинговых (ссылки и содержимое
// рекламных блоков). Селектор — самый устойчивый из однозначных:
// data-testid, name, placeholder, aria-label, #id (если не похож на
// сгенерированный), title, короткий href, затем путь с :nth-of-type от
// ближайшего предка с устойчивым атрибутом. Значения полей — начало и длина
// (не пароль; на странице с введённым секретом — нет).
var elementsJS = `((max, root, kinds, secret, valMax) => {
  const vt = ` + visibleTextJS + `;
  const base = root ? document.querySelector(root) : document;
  if (!base) return null;
  const q = 'a[href], button, input:not([type=hidden]), select, textarea, summary, [role=button], [role=link], [role=tab], ' +
    '[role=menuitem], [role=checkbox], [role=radio], [role=option], [role=switch], [role=textbox], [onclick], [contenteditable=""], [contenteditable=true]';
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none'; };
  const ad = new RegExp('` + adHosts + `', 'i');
  const adBox = /(^|[\s_-])(adfox|adv|advert|advertisement|ads|sponsor|sponsored)([\s_-]|$)/i;
  const inAd = e => { for (let x = e; x && x !== document.body; x = x.parentElement) {
      if (adBox.test(String(x.className && x.className.baseVal !== undefined ? x.className.baseVal : x.className || '')) || adBox.test(x.id || '')) return true; }
    return false; };
  const one = s => { try { return document.querySelectorAll(s).length === 1; } catch (_) { return false; } };
  const esc = v => v.replace(/["\\]/g, '\\$&');
  const attr = (e, a) => { const v = e.getAttribute(a); if (!v || v.length > 80) return ''; const s = '[' + a + '="' + esc(v) + '"]';
    return one(s) ? s : (one(e.localName + s) ? e.localName + s : ''); };
  // id вида ember123, :r1:, react-select-5-input, длинные хэши — меняются между загрузками
  const genID = id => /\d{3,}|^:|[:]|[0-9a-f]{8,}|^(ember|react|mui|radix|headlessui|rc_|id-)/i.test(id);
  const stable = e => {
    for (const a of ['data-testid', 'data-test', 'data-qa', 'name', 'placeholder', 'aria-label']) { const s = attr(e, a); if (s) return s; }
    if (e.id && !genID(e.id) && one('#' + CSS.escape(e.id))) return '#' + CSS.escape(e.id);
    for (const a of ['title', 'for']) { const s = attr(e, a); if (s) return s; }
    if (e.localName === 'a') { const s = attr(e, 'href'); if (s) return s; }
    return '';
  };
  const sel = e => {
    const s0 = stable(e); if (s0) return s0;
    const path = [];
    for (let x = e; x && x.nodeType === 1 && x !== document.documentElement; x = x.parentElement) {
      if (x !== e) { // от ближайшего предка с устойчивым селектором
        const s = stable(x) || (x.id && one('#' + CSS.escape(x.id)) ? '#' + CSS.escape(x.id) : '');
        if (s && one(s + ' > ' + path.join(' > '))) { path.unshift(s); break; } }
      let i = 1; for (let y = x.previousElementSibling; y; y = y.previousElementSibling) if (y.localName === x.localName) i++;
      path.unshift(x.localName + ':nth-of-type(' + i + ')');
      if (one(path.join(' > '))) break; // самый короткий однозначный путь
    }
    // сгенерированный id лучше длинного пути (в пределах загрузки страницы он не меняется)
    if (path.length > 3 && e.id && one('#' + CSS.escape(e.id))) return '#' + CSS.escape(e.id);
    return path.join(' > ');
  };
  const norm = s => (s || '').replace(/\s+/g, ' ').trim().slice(0, 120);
  const label = e => norm(e.getAttribute('aria-label') || (e.isContentEditable ? '' : vt(e)) || e.placeholder || e.getAttribute('title') ||
    (e.labels ? [...e.labels].map(l => vt(l)).join(' ') : '') || (e.type === 'submit' || e.type === 'button' ? e.value : ''));
  const btnType = e => e.localName === 'input' && ['submit', 'button', 'reset', 'image'].includes(e.type);
  const kindOf = e => {
    const r = e.getAttribute('role');
    if (e.localName === 'a' || r === 'link') return 'link';
    if ((e.localName === 'input' && !btnType(e)) || ['select', 'textarea'].includes(e.localName) || e.isContentEditable ||
      ['checkbox', 'radio', 'switch', 'textbox'].includes(r)) return 'field';
    return 'button';
  };
  const want = kinds && kinds.length ? new Set(kinds.map(k => k.replace(/s$/, ''))) : null;
  const out = [];
  for (const e of base.querySelectorAll(q)) {
    if (e.parentElement && e.parentElement.closest('[contenteditable=""], [contenteditable=true]')) continue; // внутри редактора
    if (!vis(e)) continue;
    const kind = kindOf(e);
    if (want && !want.has(kind)) continue;
    if (e.localName === 'a' && ad.test(e.href)) continue;
    if (kind !== 'field' && inAd(e)) continue;
    const it = {selector: sel(e), kind: kind, tag: e.localName};
    if (e.type && e.localName !== 'a') it.type = String(e.type);
    if (e.getAttribute('role')) it.role = e.getAttribute('role');
    const t = label(e); if (t) it.text = t;
    if (e.localName === 'a') it.href = e.href.length > 300 ? e.href.slice(0, 300) + '…' : e.href;
    if (e.disabled || e.getAttribute('aria-disabled') === 'true') it.disabled = true;
    if (kind === 'field') {
      if (e.type === 'checkbox' || e.type === 'radio') { if (e.checked) it.checked = true; }
      else if (e.getAttribute('aria-checked') === 'true') it.checked = true;
      else if (!secret && !(e.localName === 'input' && e.type === 'password')) {
        const v = e.localName === 'select' ? [...e.selectedOptions].map(o => o.text).join(', ') :
          typeof e.value === 'string' ? e.value : (e.isContentEditable ? e.innerText : '');
        const cs = [...(v || '')];
        if (cs.length) { it.value = cs.slice(0, valMax).join(''); it.value_length = cs.length; }
      }
    }
    out.push(it);
    if (out.length >= max) break;
  }
  return out;
})`

// Elements — интерактивные элементы текущей страницы (до MaxElements); kinds —
// fields, buttons, links (пусто — все).
func (x *Executor) Elements(ctx context.Context, root string, kinds []string) ([]Element, error) {
	for _, k := range kinds {
		if !slices.Contains(ElementKinds, k) {
			return nil, fmt.Errorf("observe: unknown kind %q (%s)", k, strings.Join(ElementKinds, ", "))
		}
	}
	if err := x.readable(ctx); err != nil {
		return nil, err
	}
	var out *[]Element
	if err := x.Engine.Eval(ctx, call(elementsJS, MaxElements, root, kinds, x.SecretOnPage(ctx), ElementValue), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, root)
	}
	return *out, nil
}

// evalIn — список из js(max, root) на читаемой странице; null (элемента root
// нет) — ErrNotFound.
func evalIn[T any](ctx context.Context, x *Executor, js string, max int, root string) ([]T, error) {
	if err := x.readable(ctx); err != nil {
		return nil, err
	}
	var out *[]T
	rj, _ := json.Marshal(root)
	if err := x.Engine.Eval(ctx, js+"("+strconv.Itoa(max)+", "+string(rj)+")", &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, root)
	}
	return *out, nil
}

// textInJS — текст первого видимого элемента по селектору (модальное окно,
// карточка), null — нет такого.
var textInJS = `((sel) => {
  const vt = ` + visibleTextJS + `;
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none'; };
  const all = [...document.querySelectorAll(sel)];
  const e = all.find(vis) || all[0];
  return e ? vt(e) : null;
})`

// TextIn — текст элемента по селектору (вместо текста всей страницы).
func (x *Executor) TextIn(ctx context.Context, sel string) (string, error) {
	if err := x.readable(ctx); err != nil {
		return "", err
	}
	var t *string
	sj, _ := json.Marshal(sel)
	if err := x.Engine.Eval(ctx, textInJS+"("+string(sj)+")", &t); err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, sel)
	}
	return strings.TrimSpace(*t), nil
}

// Node — узел структуры страницы для outline: контейнеры с id, role,
// data-testid и смысловые теги — чтобы найти селектор блока (диалог, список).
type Node struct {
	Depth     int    `json:"depth"`
	Selector  string `json:"selector"`
	Tag       string `json:"tag"`
	Role      string `json:"role,omitempty"`
	Text      string `json:"text,omitempty"`      // начало видимого текста узла (до 200 символов)
	Truncated bool   `json:"truncated,omitempty"` // текст длиннее
	Children  int    `json:"children"`            // дочерних элементов
}

// MaxNodes — предел узлов outline.
const MaxNodes = 300

// outlineJS — видимые контейнеры до глубины 8 от root: узел попадает в
// список, если у него есть id, role, data-testid или это смысловой тег.
var outlineJS = `((max, root) => {
  const vt = ` + visibleTextJS + `;
  const base = root ? document.querySelector(root) : document.body;
  if (!base) return null;
  const tags = new Set(['main','nav','header','footer','aside','section','article','form','dialog','table','ul','ol','iframe']);
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none'; };
  const one = s => { try { return document.querySelectorAll(s).length === 1; } catch (_) { return false; } };
  const sel = e => {
    if (e.id && one('#' + CSS.escape(e.id))) return '#' + CSS.escape(e.id);
    for (const a of ['data-testid', 'data-test', 'data-qa', 'aria-label', 'name']) {
      const v = e.getAttribute(a); if (!v) continue;
      const s = e.localName + '[' + a + '="' + v.replace(/["\\]/g, '\\$&') + '"]'; if (one(s)) return s; }
    const r = e.getAttribute('role'); if (r && one('[role="' + r + '"]')) return '[role="' + r + '"]';
    return '';
  };
  const out = [];
  const walk = (e, d) => {
    if (out.length >= max || d > 8 || !vis(e)) return;
    const s = sel(e);
    const keep = s || tags.has(e.localName);
    if (keep) out.push({depth: d, selector: s, tag: e.localName, role: e.getAttribute('role') || '',
      text: vt(e).replace(/\s+/g, ' ').trim().slice(0, 200), truncated: vt(e).replace(/\s+/g, ' ').trim().length > 200, children: e.children.length});
    for (const c of e.children) walk(c, keep ? d + 1 : d);
  };
  walk(base, 0);
  return out;
})`

// Outline — структура страницы (или элемента root): контейнеры с селекторами.
func (x *Executor) Outline(ctx context.Context, root string) ([]Node, error) {
	return evalIn[Node](ctx, x, outlineJS, MaxNodes, root)
}

// Request — запрос страницы к серверу (fetch / XHR), как его видит агент,
// который ищет API сайта.
type Request struct {
	Method    string `json:"method,omitempty"`
	URL       string `json:"url"`
	Status    int    `json:"status,omitempty"`
	Kind      string `json:"kind"` // fetch | xhr
	MIME      string `json:"mime,omitempty"`
	Body      string `json:"body,omitempty"` // ответ (JSON или текст, до MaxBody символов, секреты скрыты)
	Truncated bool   `json:"truncated,omitempty"`
}

// Пределы network.
const (
	MaxRequests = 100
	MaxBody     = 2000
)

// netHookJS — перехват fetch и XHR страницы: метод, адрес, статус, тип и
// начало ответа (JSON и текст) — в window.__gate4aiNet (последние 100).
// Тела запросов не пишутся: в них бывают пароли, которые человек вводит сам.
// Повторная установка ничего не меняет.
const netHookJS = `(() => {
  if (window.__gate4aiNet) return true;
  const log = window.__gate4aiNet = [];
  const add = r => { log.push(r); if (log.length > 100) log.shift(); };
  const text = ct => /json|text\/plain|javascript/.test(ct || '');
  const f = window.fetch;
  if (f) window.fetch = function(input, init) {
    const method = ((init && init.method) || (input && input.method) || 'GET').toUpperCase();
    const url = String((input && input.url) || input);
    return f.apply(this, arguments).then(resp => {
      const ct = resp.headers.get('content-type') || '';
      const r = {kind: 'fetch', method, url: resp.url || url, status: resp.status, mime: ct};
      add(r);
      if (text(ct)) resp.clone().text().then(b => { r.body = b.slice(0, 4000); r.size = b.length; }).catch(() => {});
      return resp;
    });
  };
  const open = XMLHttpRequest.prototype.open, send = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function(method, url) { this.__g4 = {kind: 'xhr', method: String(method).toUpperCase(), url: String(url)}; return open.apply(this, arguments); };
  XMLHttpRequest.prototype.send = function() {
    const r = this.__g4;
    if (r) this.addEventListener('loadend', () => {
      r.status = this.status; r.url = this.responseURL || r.url; r.mime = this.getResponseHeader('content-type') || '';
      if (text(r.mime)) {
        // HttpClient (Angular) и axios берут ответ как json: responseText тогда недоступен
        let b = '';
        try { b = (this.responseType === '' || this.responseType === 'text') ? this.responseText
          : this.responseType === 'json' ? JSON.stringify(this.response) : ''; } catch (_) {}
        if (b) { r.body = b.slice(0, 4000); r.size = b.length; }
      }
      add(r);
    });
    return send.apply(this, arguments);
  };
  return true;
})()`

// netListJS — перехваченные запросы и запросы до перехвата (при загрузке
// страницы) из Performance API: у них нет метода и тела.
const netListJS = `(() => {
  const log = (window.__gate4aiNet || []).map(r => Object.assign({}, r));
  const seen = new Set(log.map(r => r.url));
  const early = performance.getEntriesByType('resource')
    .filter(e => (e.initiatorType === 'fetch' || e.initiatorType === 'xmlhttprequest') && !seen.has(e.name))
    .map(e => ({kind: e.initiatorType === 'fetch' ? 'fetch' : 'xhr', url: e.name, status: e.responseStatus || 0}));
  return early.concat(log);
})()`

// HookNetwork ставит перехват запросов на текущей странице (после перехода
// и действий; ошибки не мешают самому действию).
func (x *Executor) HookNetwork(ctx context.Context) {
	if _, ok := x.Engine.(netEngine); ok {
		return // запросы видны по CDP Network — страницу не трогаем
	}
	var ok bool
	_ = x.Engine.Eval(ctx, netHookJS, &ok)
}

// netEngine — движок, который видит запросы вкладки сам (CDP Network): тела
// любых ответов, странице ничего не подменяется.
type netEngine interface {
	NetRequests(ctx context.Context) ([]Request, []int)
}

// Network — запросы страницы к серверу; contains — фильтр по адресу; all —
// и к сайтам вне списка (иначе только к разрешённым).
func (x *Executor) Network(ctx context.Context, contains string, all bool) ([]Request, error) {
	u, err := x.Engine.URL(ctx)
	if err != nil {
		return nil, err
	}
	if err := x.CheckURL(u); err != nil {
		return nil, err
	}
	if err := x.readable(ctx); err != nil {
		return nil, err
	}
	type rawReq struct {
		Request
		Size int `json:"size"`
	}
	var raw []rawReq
	if ne, ok := x.Engine.(netEngine); ok {
		reqs, sizes := ne.NetRequests(ctx)
		for i, q := range reqs {
			raw = append(raw, rawReq{Request: q, Size: sizes[i]})
		}
	} else if err := x.Engine.Eval(ctx, netListJS, &raw); err != nil {
		return nil, err
	}
	out := []Request{}
	for _, r := range raw {
		if contains != "" && !strings.Contains(r.URL, contains) {
			continue
		}
		if !all && x.CheckURL(r.URL) != nil {
			continue // счётчики и чужие сервисы: не «запросы сайта»
		}
		q := r.Request
		q.URL = x.redactURL(q.URL)
		if q.Body != "" {
			q.Body = x.redactValues(RedactBody(q.URL, q.Body))
			rs := []rune(q.Body)
			if len(rs) > MaxBody {
				q.Body = string(rs[:MaxBody])
			}
			q.Truncated = len(rs) > MaxBody || r.Size > len([]rune(r.Body))
		}
		out = append(out, q)
	}
	if len(out) > MaxRequests {
		out = out[len(out)-MaxRequests:]
	}
	return out, nil
}

// sensitiveKey — поля ответа, значения которых агенту не показываются.
var sensitiveKey = regexp.MustCompile(`(?i)token|secret|passw|session|cookie|authoriz|^auth|csrf|xsrf|cvv|cvc|card_?num|^pin$|_pin$|^otp$|_otp$`)

// sensitiveField — то же в тексте, который не разобрать как JSON (ответ обрезан).
var sensitiveField = regexp.MustCompile(`(?i)("[^"]*(token|secret|passw|session|cookie|authoriz|csrf|xsrf|cvv|cvc)[^"]*"\s*:\s*)"[^"]*"`)

// authURL — ответы входа и выдачи токенов не показываются целиком.
var authURL = regexp.MustCompile(`(?i)login|logout|signin|sign-in|oauth|token|auth|password|session|otp|2fa|mfa`)

// RedactBody скрывает ответы входа и значения чувствительных полей JSON
// (токены, пароли, сессии, номера карт): в ответах API сайта их видеть
// агенту не нужно — коннектор входит через учётку, а не через токен.
func RedactBody(url, body string) string {
	if authURL.MatchString(url) {
		return "[hidden: response of a sign-in or token request]"
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return sensitiveField.ReplaceAllString(body, `$1"***"`)
	}
	b, _ := json.Marshal(redactKeys(v, sensitiveKey.MatchString))
	return string(b)
}

// redactKeys заменяет на *** значения полей JSON, для которых hide истинно.
func redactKeys(v any, hide func(string) bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if hide(k) {
				t[k] = "***" // и объект целиком: {"session": {"id": …, "key": …}}
				continue
			}
			t[k] = redactKeys(x, hide)
		}
	case []any:
		for i := range t {
			t[i] = redactKeys(t[i], hide)
		}
	}
	return v
}

// redactURL — значения параметров запроса из RedactParams (идентификаторы,
// которые владелец считает личными: ehrId) и секретных параметров заменяются.
func (x *Executor) redactURL(u string) string {
	return RedactURL(u, x.redacted)
}

// RedactURL заменяет на *** значения параметров запроса, для которых hide
// (или SecretParam) истинно; порядок и остальные параметры — как были. Так же
// разбирается фрагмент: токены implicit-входа OAuth и OIDC приходят в нём
// (#access_token=…&id_token=…), у маршрутов SPA — после «?» (#/cb?code=…).
func RedactURL(u string, hide func(string) bool) string {
	rest, frag, hasFrag := strings.Cut(u, "#")
	base, query, hasQuery := strings.Cut(rest, "?")
	out := base
	if hasQuery {
		out += "?" + redactPairs(query, hide)
	}
	if hasFrag {
		if route, q, ok := strings.Cut(frag, "?"); ok {
			frag = route + "?" + redactPairs(q, hide)
		} else if strings.Contains(frag, "=") {
			frag = redactPairs(frag, hide)
		}
		out += "#" + frag
	}
	return out
}

// redactPairs — то же для строки k=v&k=v.
func redactPairs(query string, hide func(string) bool) string {
	parts := strings.Split(query, "&")
	for i, p := range parts {
		k, _, hasVal := strings.Cut(p, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if hasVal && (SecretParam(name) || (hide != nil && hide(name))) {
			parts[i] = k + "=***"
		}
	}
	return strings.Join(parts, "&")
}

func (x *Executor) redacted(k string) bool {
	return slices.ContainsFunc(x.RedactParams, func(p string) bool { return strings.EqualFold(p, k) })
}

// redactValues — поля JSON с именами из RedactParams.
func (x *Executor) redactValues(body string) string {
	if len(x.RedactParams) == 0 {
		return body
	}
	var v any
	if json.Unmarshal([]byte(body), &v) != nil {
		return body
	}
	b, _ := json.Marshal(redactKeys(v, x.redacted))
	return string(b)
}

// secretParam — параметры адреса с одноразовыми кодами и токенами (OAuth).
var secretParam = regexp.MustCompile(`(?i)^(code|token|access_token|id_token|refresh_token|state|ticket|sid|session.*|auth.*|.*_token)$`)

// SecretParam — параметр адреса, значение которого не показывается.
func SecretParam(k string) bool { return secretParam.MatchString(k) }
