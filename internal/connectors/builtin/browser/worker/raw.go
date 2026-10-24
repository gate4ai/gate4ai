// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Body — ответ как его прислал сервер (документ не HTML, ответ fetch):
// разобранный JSON или текст, тип и размер; секреты скрыты, как в network.
type Body struct {
	ContentType string `json:"content_type,omitempty"`
	JSON        any    `json:"json,omitempty"`      // ответ JSON целиком (не обрезан)
	Body        string `json:"body,omitempty"`      // иначе текст (до предела символов)
	Size        int    `json:"size"`                // байт в ответе
	Truncated   bool   `json:"truncated,omitempty"` // body обрезан
	// Hint — у обрезанного тела: сколько символов в нём целиком и какой
	// max_chars вернёт его полностью (JSON — разобранным).
	Hint string `json:"hint,omitempty"`
}

// docInfoJS — тип верхнего документа и его текст без просмотрщика Chrome:
// просмотрщик JSON и текста оставляет исходный ответ в первом <pre>.
const docInfoJS = `(() => {
  const ct = document.contentType || '';
  if (/^(text\/html|application\/xhtml)/i.test(ct)) return {type: ct, html: true};
  const pre = document.querySelector('body > pre');
  return {type: ct, html: false, body: pre ? pre.textContent : (document.body ? document.body.textContent : '')};
})()`

// docBodier — движок, который отдаёт тело ответа верхнего документа (CDP Network).
type docBodier interface {
	DocumentBody(ctx context.Context) (string, bool)
}

// RawDocument — верхний документ не HTML (JSON, текст): его тело как есть до
// max символов (0 — по умолчанию, см. Limits; text — предел текста по
// умолчанию); ok — false у HTML-страницы.
func (x *Executor) RawDocument(ctx context.Context, max, text int) (b Body, ok bool, err error) {
	u, err := x.settledURL(ctx)
	if err != nil {
		return Body{}, false, err
	}
	var info struct {
		Type string `json:"type"`
		HTML bool   `json:"html"`
		Body string `json:"body"`
	}
	if err := x.Engine.Eval(ctx, docInfoJS, &info); err != nil || info.HTML || info.Type == "" {
		return Body{}, false, nil // движок не ответил о типе — страница как обычно
	}
	if err := x.readable(ctx); err != nil {
		return Body{}, false, err
	}
	body := info.Body
	if db, ok := x.Engine.(docBodier); ok {
		if raw, ok := db.DocumentBody(ctx); ok {
			body = raw
		}
	}
	textMax, jsonMax := Limits(max, text)
	return x.body(u, info.Type, body, len(body), 0, textMax, jsonMax, false), true, nil
}

// MaxFetchResponse — предел тела ответа не JSON по умолчанию (символов).
const MaxFetchResponse = 30000

// DefaultJSONChars — предел тела ответа JSON по умолчанию (символов):
// обрезанный JSON не разбирается, поэтому предел у него выше.
const DefaultJSONChars = 200000

// MaxResponseChars — верхняя граница max_chars (символов).
const MaxResponseChars = 1000000

// Limits — пределы тела ответа для текста и для JSON: n — явный max_chars
// (больше верхней границы — граница), 0 — по умолчанию: text (предел
// max_text_chars настроек, 0 — MaxFetchResponse) и не меньше DefaultJSONChars у JSON.
func Limits(n, text int) (textMax, jsonMax int) {
	if n > 0 {
		n = min(n, MaxResponseChars)
		return n, n
	}
	if text <= 0 {
		text = MaxFetchResponse
	}
	text = min(text, MaxResponseChars)
	return text, min(MaxResponseChars, max(text, DefaultJSONChars))
}

// body — ответ для агента: секреты скрыты (RedactBody, redact_params); parse
// — JSON разобран, если он целиком в пределах jsonMax символов; иначе текст
// до предела (textMax, у JSON — jsonMax) с подсказкой, сколько в нём
// символов (chars — длина до обрезки на странице, 0 — raw целиком).
func (x *Executor) body(u, ct, raw string, size, chars, textMax, jsonMax int, parse bool) Body {
	b := Body{ContentType: ct, Size: size}
	if raw == "" {
		return b
	}
	text := x.redactValues(RedactBody(x.redactURL(u), raw))
	lim := textMax
	if jsonMIME(ct) {
		lim = jsonMax
	}
	if n := utf8.RuneCountInString(text); lim > 0 && n > lim {
		b.Body, b.Truncated = string([]rune(text)[:lim]), true
		b.Hint = truncHint(lim, max(n, chars), parse && jsonMIME(ct))
		return b
	}
	var v any
	if parse && jsonMIME(ct) && json.Unmarshal([]byte(text), &v) == nil {
		b.JSON = v
		return b
	}
	b.Body = text
	return b
}

// truncHint — подсказка у обрезанного тела: обрезано на lim из total символов
// и какой max_chars нужен для полного ответа (больше верхней границы — сузить запрос).
func truncHint(lim, total int, json bool) string {
	h := fmt.Sprintf("body truncated at %d of %d chars, pass max_chars ≥ %d", lim, total, total)
	if total > MaxResponseChars {
		h = fmt.Sprintf("body truncated at %d of %d chars, more than the max_chars limit %d: narrow the request (filters, paging)", lim, total, MaxResponseChars)
	}
	if json {
		h = "json not parsed: " + h
	}
	return h
}

func jsonMIME(ct string) bool { return strings.Contains(strings.ToLower(ct), "json") }

// readableMIME — ответ, который показывается текстом (не картинка, не архив).
func readableMIME(ct string) bool {
	ct = strings.ToLower(ct)
	return ct == "" || textMIME(ct) || strings.HasPrefix(ct, "text/") || strings.Contains(ct, "xml") || strings.Contains(ct, "x-www-form-urlencoded")
}

// FetchRequest — запрос со страницы сеанса (browser.get, post…): от её
// origin, с её cookies.
type FetchRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    *string // nil — без тела (GET, HEAD)
	Max     int     // символов тела ответа (max_chars; 0 — по умолчанию, см. Limits)
	TextMax int     // предел текста по умолчанию (max_text_chars; 0 — MaxFetchResponse)
}

// FetchResponse — ответ запроса: статус, адрес после редиректов, заголовки
// (без cookies и авторизации) и тело.
type FetchResponse struct {
	Status  int               `json:"status"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	// Redirect — сервер ответил редиректом на запрос с эффектом: он не
	// повторён (метод и тело не уходят на другой адрес).
	Redirect bool `json:"redirect,omitempty"`
	Body
}

// Fetch-методы: GET и HEAD — чтение, остальные меняют данные.
var FetchMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// MaxFetchBody — предел тела запроса (символов), как у value в act.
const MaxFetchBody = MaxActValue

// fetchJS — запрос со страницы: fetch с cookies пользователя. Редиректы
// чтения следуют (адрес проверяет Fetch), запросы с эффектом — нет (manual):
// 307 / 308 повторили бы метод и тело на другом адресе.
const fetchJS = `(async (url, method, headers, body, max) => {
  try {
    const init = {method, headers: headers || {}, credentials: 'include', redirect: (method === 'GET' || method === 'HEAD') ? 'follow' : 'manual'};
    if (body !== null) init.body = body;
    const r = await fetch(url, init);
    const hs = {}; r.headers.forEach((v, k) => { hs[k] = v; });
    const out = {status: r.status, url: r.url || url, redirect: r.type === 'opaqueredirect', headers: hs, size: 0, body: ''};
    if (method === 'HEAD' || r.type === 'opaqueredirect') return out;
    const buf = await r.arrayBuffer();
    out.size = buf.byteLength;
    const ct = hs['content-type'] || '';
    if (ct === '' || /json|javascript|xml|x-www-form-urlencoded|^text\//i.test(ct)) {
      const t = new TextDecoder().decode(buf);
      out.chars = t.length;
      out.body = t.length > max + 1 ? t.slice(0, max + 1) : t;
    }
    return out;
  } catch (e) {
    return {error: String((e && e.message) || e)};
  }
})`

// hiddenHeader — заголовки ответа, которые агенту не показываются.
var hiddenHeader = regexp.MustCompile(`(?i)cookie|authoriz|authenticat|token|secret|session|csrf|xsrf|^x-api-key$`)

// ErrFetch — запрос со страницы не дошёл до сервера (сеть, CORS, адрес).
var ErrFetch = errors.New("the request from the page failed")

// Fetch выполняет запрос изнутри страницы сеанса: адрес — по списку сайтов
// (до и после редиректов), страница — на разрешённом сайте.
func (x *Executor) Fetch(ctx context.Context, r FetchRequest) (FetchResponse, error) {
	if !slices.Contains(FetchMethods, r.Method) {
		return FetchResponse{}, fmt.Errorf("fetch: unknown method %q", r.Method)
	}
	if r.Body != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return FetchResponse{}, fmt.Errorf("fetch: %s has no body", r.Method)
	}
	if r.Body != nil && utf8.RuneCountInString(*r.Body) > MaxFetchBody {
		return FetchResponse{}, fmt.Errorf("argument \"body\" is too long: %d characters, the limit is %d", utf8.RuneCountInString(*r.Body), MaxFetchBody)
	}
	if err := x.CheckURL(r.URL); err != nil {
		return FetchResponse{}, err
	}
	textMax, jsonMax := Limits(r.Max, r.TextMax)
	page, err := x.settledURL(ctx)
	if err != nil {
		return FetchResponse{}, err
	}
	if !strings.HasPrefix(page, "http") {
		return FetchResponse{}, errors.New("fetch: the page is blank; open a page of the site first (its cookies and origin are used)")
	}
	var body any
	if r.Body != nil {
		body = *r.Body
	}
	var out struct {
		FetchResponse
		Size  int    `json:"size"`
		Chars int    `json:"chars"`
		Body  string `json:"body"`
		Error string `json:"error"`
	}
	if err := x.Engine.Eval(ctx, call(fetchJS, r.URL, r.Method, r.Headers, body, jsonMax), &out); err != nil {
		return FetchResponse{}, err
	}
	if out.Error != "" {
		return FetchResponse{}, fmt.Errorf("%w: %s (a site other than the page's needs CORS; open a page of that site first)", ErrFetch, out.Error)
	}
	res := out.FetchResponse
	if res.URL == "" {
		res.URL = r.URL
	}
	if err := x.CheckURL(res.URL); err != nil {
		return FetchResponse{}, fmt.Errorf("the request was redirected off the allowlist, its response is not shown: %w", err)
	}
	hs := map[string]string{}
	for k, v := range res.Headers {
		if !hiddenHeader.MatchString(k) {
			hs[k] = v
		}
	}
	res.Headers = hs
	res.Body = x.body(res.URL, res.Headers["content-type"], out.Body, out.Size, out.Chars, textMax, jsonMax, true)
	if out.Body == "" && out.Size > 0 && !readableMIME(res.Headers["content-type"]) {
		res.Body.Body = "" // картинка, архив: только тип и размер
	}
	res.URL = x.redactURL(res.URL)
	return res, nil
}
