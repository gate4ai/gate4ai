// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Загрузки. Chrome по клику на «Скачать» сохраняет файл сам — или открывает
// системный диалог «Сохранить как», которого агент не видит, а страница
// сеанса ждёт его. Воркер держит своё подключение к браузеру (Browser.*) и
// ставит Browser.setDownloadBehavior: обычные загрузки (клик агента в act,
// клик человека в окне) молча ложатся в папку загрузок пользователя, диалога
// нет; на время browser.download — во временную папку под guid загрузки,
// откуда файл читается и удаляется. Поведение действует, пока подключение
// живо, поэтому оно постоянное (Service.downloads).

// MaxDownloadBytes — предел файла browser.download (байт): файл идёт агенту
// в ответе и копией в доказательства (одно сообщение пакета — до 64 МБ, base64).
const MaxDownloadBytes = 20 << 20

// ErrTooLarge — файл больше предела.
var ErrTooLarge = errors.New("the file is larger than the download limit")

// ErrFileType — тип файла не входит в разрешённые владельцем (download_types).
var ErrFileType = errors.New("the file type is not allowed")

// DefaultDownloadWait — сколько ждать файл после клика по умолчанию; MaxDownloadWait — предел.
const (
	DefaultDownloadWait = 30 * time.Second
	MaxDownloadWait     = 120 * time.Second
)

// DownloadRequest — browser.download: клик по элементу страницы (Selector,
// Text) или запрос со страницы (URL).
type DownloadRequest struct {
	Selector string
	Text     string
	URL      string
	// ResponseURLContains — файл — ответ страницы на её же запрос (fetch /
	// XHR) с этим в адресе: для сайтов, которые не скачивают файл, а
	// показывают его (blob в новой вкладке). Страница сама ставит свои
	// заголовки (токены), агенту они не видны.
	ResponseURLContains string
	TimeoutMS           int
	MaxBytes            int      // предел владельца (0 — MaxDownloadBytes)
	Types               []string // разрешённые типы (MIME, MIME/*, .расширение); пусто — любые
}

// File — скачанный файл: имя, тип, байты, sha256 и откуда он (via:
// download — загрузка Chrome, response — ответ страницы, fetch — запрос со страницы).
type File struct {
	Name   string
	MIME   string
	URL    string // адрес файла (http/https; data: и blob: не показываются)
	Via    string
	Data   []byte
	SHA256 string
}

// CheckDownload проверяет аргументы до страницы.
func CheckDownload(r DownloadRequest) error {
	switch {
	case r.Selector == "" && r.URL == "":
		return errors.New("download: pass selector (the element that starts the download) or url (a file on an allowed site)")
	case r.Selector != "" && r.URL != "":
		return errors.New("download: pass either selector or url, not both")
	case r.URL != "" && (r.Text != "" || r.ResponseURLContains != ""):
		return errors.New("download: text and response_url_contains go with selector")
	case r.TimeoutMS < 0 || time.Duration(r.TimeoutMS)*time.Millisecond > MaxDownloadWait:
		return fmt.Errorf("download: timeout_ms is 0…%d", MaxDownloadWait.Milliseconds())
	}
	return nil
}

func (r DownloadRequest) max() int {
	if r.MaxBytes <= 0 || r.MaxBytes > MaxDownloadBytes {
		return MaxDownloadBytes
	}
	return r.MaxBytes
}

// Download скачивает файл: клик по элементу (загрузка Chrome или ответ
// страницы) или GET со страницы сеанса (её cookies). Адреса — по списку
// сайтов; тип и размер — по пределам владельца.
func (ss *Session) Download(ctx context.Context, r DownloadRequest) (File, error) {
	if err := CheckDownload(r); err != nil {
		return File{}, err
	}
	var f File
	var err error
	if r.URL != "" {
		f, err = ss.Executor.fetchFile(ctx, r.URL, r.max())
	} else {
		f, err = ss.clickDownload(ctx, r)
	}
	if err != nil {
		return File{}, err
	}
	return finish(f, r)
}

// responseCatcher — движок перехватывает ответы страницы (CDP Fetch).
type responseCatcher interface {
	CatchResponses(ctx context.Context, contains string) (ResponseCatch, error)
}

// ResponseCatch — идущий перехват: Next — тело первого ответа (ok — false,
// пока его нет), Stop — конец перехвата.
type ResponseCatch interface {
	Next(ctx context.Context, max int) (File, bool, error)
	Stop()
}

func (ss *Session) clickDownload(ctx context.Context, r DownloadRequest) (File, error) {
	x := ss.Executor
	var rc ResponseCatch
	if r.ResponseURLContains != "" {
		ce, ok := x.Engine.(responseCatcher)
		if !ok {
			return File{}, errors.New("download: response_url_contains needs the cdp engine (connectors.browser.engine: cdp)")
		}
		var err error
		if rc, err = ce.CatchResponses(ctx, r.ResponseURLContains); err != nil {
			return File{}, fmt.Errorf("download: %w", err)
		}
		defer rc.Stop()
	}
	d := ss.s.downloadsConn()
	if d == nil && rc == nil {
		return File{}, errors.New("download: Chrome downloads are not available (no DevTools connection to the browser); pass response_url_contains to take the file from the page's response")
	}
	var c *capture
	if d != nil {
		var err error
		if c, err = d.begin(ctx); err != nil {
			return File{}, err
		}
		defer c.end()
	}
	if _, err := x.Act(ctx, ActRequest{Selector: r.Selector, Text: r.Text, Method: "click", TimeoutMS: r.TimeoutMS}); err != nil {
		return File{}, err
	}
	timeout := time.Duration(r.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultDownloadWait
	}
	deadline := time.Now().Add(timeout)
	for {
		if c != nil {
			f, done, err := c.poll(ctx, r.max())
			if err != nil || done {
				return f, err
			}
		}
		if rc != nil {
			f, ok, err := rc.Next(ctx, r.max())
			if err != nil || ok {
				return f, err
			}
		}
		if time.Now().After(deadline) {
			if c != nil && c.started() {
				return File{}, fmt.Errorf("download: the file did not finish downloading in %s (pass a larger timeout_ms, up to %d)", timeout, MaxDownloadWait.Milliseconds())
			}
			hint := "the element may open the file in a tab instead: find its request with browser.network and pass response_url_contains"
			if rc != nil {
				hint = "no request matching response_url_contains " + fmt.Sprintf("%q", r.ResponseURLContains) + " finished either"
			}
			return File{}, fmt.Errorf("%w: the click started no download in %s; %s", ErrNotFound, timeout, hint)
		}
		if err := wait(ctx, 100*time.Millisecond); err != nil {
			return File{}, err
		}
	}
}

// fetchFileJS — GET со страницы (её cookies), тело — base64; больше max байт —
// только размер.
const fetchFileJS = `(async (url, max) => {
  try {
    const r = await fetch(url, {credentials: 'include', redirect: 'follow'});
    const out = {status: r.status, url: r.url || url, type: r.headers.get('content-type') || '', disposition: r.headers.get('content-disposition') || ''};
    if (!r.ok) return out;
    const len = Number(r.headers.get('content-length') || 0);
    if (len > max) { out.size = len; return out; }
    const buf = new Uint8Array(await r.arrayBuffer());
    out.size = buf.length;
    if (buf.length > max) return out;
    let s = '';
    for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
    out.data = btoa(s);
    return out;
  } catch (e) {
    return {error: String((e && e.message) || e)};
  }
})`

// fetchFile — файл по адресу запросом со страницы сеанса (как browser.get, но
// тело — байты).
func (x *Executor) fetchFile(ctx context.Context, u string, max int) (File, error) {
	if err := x.CheckURL(u); err != nil {
		return File{}, err
	}
	page, err := x.settledURL(ctx)
	if err != nil {
		return File{}, err
	}
	if !strings.HasPrefix(page, "http") {
		return File{}, errors.New("download: the page is blank; open a page of the site first (its cookies and origin are used)")
	}
	var out struct {
		Status      int    `json:"status"`
		URL         string `json:"url"`
		Type        string `json:"type"`
		Disposition string `json:"disposition"`
		Size        int    `json:"size"`
		Data        string `json:"data"`
		Error       string `json:"error"`
	}
	if err := x.Engine.Eval(ctx, call(fetchFileJS, u, max), &out); err != nil {
		return File{}, err
	}
	if out.Error != "" {
		return File{}, fmt.Errorf("%w: %s (a site other than the page's needs CORS; open a page of that site first)", ErrFetch, out.Error)
	}
	if out.URL == "" {
		out.URL = u
	}
	if err := x.CheckURL(out.URL); err != nil {
		return File{}, fmt.Errorf("the request was redirected off the allowlist, its file is not returned: %w", err)
	}
	if out.Status < 200 || out.Status > 299 {
		return File{}, fmt.Errorf("download: %s answered %d (a file the page gets with its own headers — click its button: download --selector, or --response_url_contains)", x.redactURL(out.URL), out.Status)
	}
	if out.Size > max {
		return File{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, out.Size, max)
	}
	data, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return File{}, err
	}
	name := dispositionName(out.Disposition)
	if name == "" {
		if pu, err := url.Parse(out.URL); err == nil {
			name = path.Base(pu.Path)
		}
	}
	return File{Name: name, MIME: out.Type, URL: x.redactURL(out.URL), Via: "fetch", Data: data}, nil
}

// finish — имя без пути, тип (по заголовку, расширению или содержимому),
// sha256; тип и размер — по пределам владельца.
func finish(f File, r DownloadRequest) (File, error) {
	if len(f.Data) > r.max() {
		return File{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, len(f.Data), r.max())
	}
	if !strings.HasPrefix(f.URL, "http://") && !strings.HasPrefix(f.URL, "https://") {
		f.URL = "" // data: и blob: — сам файл или ссылка на память страницы
	}
	f.MIME = mediaType(f.MIME)
	if f.MIME == "" || f.MIME == "application/octet-stream" || f.MIME == "binary/octet-stream" {
		if t := mediaType(mime.TypeByExtension(strings.ToLower(path.Ext(f.Name)))); t != "" {
			f.MIME = t
		} else {
			f.MIME = mediaType(http.DetectContentType(f.Data))
		}
	}
	f.Name = fileName(f.Name, f.MIME)
	if !typeAllowed(f, r.Types) {
		return File{}, fmt.Errorf("%w: %s (%s); the owner allows %s (connectors.browser.download_types)", ErrFileType, f.Name, f.MIME, strings.Join(r.Types, ", "))
	}
	sum := sha256.Sum256(f.Data)
	f.SHA256 = hex.EncodeToString(sum[:])
	return f, nil
}

// dispositionName — имя файла из Content-Disposition ("" — нет).
func dispositionName(v string) string {
	if _, p, err := mime.ParseMediaType(v); err == nil {
		return p["filename"]
	}
	return ""
}

func mediaType(t string) string {
	mt, _, err := mime.ParseMediaType(t)
	if err != nil {
		return ""
	}
	return mt
}

// fileName — имя файла без каталогов и управляющих символов; без расширения —
// с расширением по типу (сайты отдают «Анализ мочи» вместо «Анализ мочи.pdf»).
func fileName(name, mt string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "download"
	}
	if path.Ext(name) == "" {
		if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
			ext := exts[0]
			for _, e := range exts {
				if e == ".pdf" || e == ".csv" || e == ".zip" || e == ".xlsx" {
					ext = e
				}
			}
			name += ext
		}
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[len(r)-200:])
	}
	return name
}

// typeAllowed — тип файла в списке владельца: MIME (application/pdf), группа
// (image/*) или расширение (.csv); пустой список — любой.
func typeAllowed(f File, types []string) bool {
	if len(types) == 0 {
		return true
	}
	ext := strings.ToLower(path.Ext(f.Name))
	for _, t := range types {
		t = strings.ToLower(strings.TrimSpace(t))
		switch {
		case strings.HasPrefix(t, "."):
			if ext == t {
				return true
			}
		case strings.HasSuffix(t, "/*"):
			if strings.HasPrefix(f.MIME, strings.TrimSuffix(t, "*")) {
				return true
			}
		case t == f.MIME:
			return true
		}
	}
	return false
}

// Downloads — подключение к браузеру, через которое идут загрузки.
type Downloads struct {
	c    *CDP
	dir  string     // папка обычных загрузок (пользователя)
	busy sync.Mutex // один browser.download за раз
	mu   sync.Mutex
	cur  *capture
}

// dlEvent — Browser.downloadWillBegin / Browser.downloadProgress.
type dlEvent struct {
	method string
	GUID   string  `json:"guid"`
	URL    string  `json:"url"`
	Name   string  `json:"suggestedFilename"`
	State  string  `json:"state"` // inProgress, completed, canceled
	Total  float64 `json:"totalBytes"`
	Got    float64 `json:"receivedBytes"`
}

// DialDownloads подключается к браузеру и включает загрузки без диалога в dir.
func DialDownloads(ctx context.Context, httpURL, dir string) (*Downloads, error) {
	d := &Downloads{dir: dir}
	c, err := dialBrowser(ctx, httpURL, d.event)
	if err != nil {
		return nil, err
	}
	d.c = c
	if err := d.behave(ctx, "allow", dir); err != nil {
		c.Close()
		return nil, err
	}
	return d, nil
}

func (d *Downloads) behave(ctx context.Context, behavior, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("downloads folder: %w", err)
	}
	return d.c.call(ctx, "", "Browser.setDownloadBehavior", map[string]any{"behavior": behavior, "downloadPath": dir, "eventsEnabled": true}, nil)
}

func (d *Downloads) event(m cdpMsg) {
	if m.Method != "Browser.downloadWillBegin" && m.Method != "Browser.downloadProgress" {
		return
	}
	var e dlEvent
	if json.Unmarshal(m.Params, &e) != nil {
		return
	}
	e.method = m.Method
	d.mu.Lock()
	c := d.cur
	d.mu.Unlock()
	if c != nil {
		select {
		case c.events <- e:
		default:
		}
	}
}

// Alive — подключение к браузеру живо.
func (d *Downloads) Alive() bool { return d != nil && !d.c.lost() }

// Close закрывает подключение (Chrome возвращает своё поведение загрузок).
func (d *Downloads) Close() { d.c.Close() }

// capture — загрузки одного browser.download: во временной папке под guid.
type capture struct {
	d      *Downloads
	dir    string
	events chan dlEvent
	guid   string // первая начавшаяся загрузка
	name   string
	url    string
}

// begin — загрузки идут во временную папку (allowAndName), события — в capture.
func (d *Downloads) begin(ctx context.Context) (*capture, error) {
	d.busy.Lock()
	dir, err := os.MkdirTemp("", "gate4ai-download-")
	if err != nil {
		d.busy.Unlock()
		return nil, err
	}
	c := &capture{d: d, dir: dir, events: make(chan dlEvent, 256)}
	d.mu.Lock()
	d.cur = c
	d.mu.Unlock()
	if err := d.behave(ctx, "allowAndName", dir); err != nil {
		c.end()
		return nil, fmt.Errorf("download: %w", err)
	}
	return c, nil
}

// end — незаконченная загрузка отменяется, поведение — обратно к папке
// пользователя, временная папка удаляется.
func (c *capture) end() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if c.guid != "" {
		_ = c.d.c.call(ctx, "", "Browser.cancelDownload", map[string]any{"guid": c.guid}, nil)
	}
	_ = c.d.behave(ctx, "allow", c.d.dir)
	c.d.mu.Lock()
	c.d.cur = nil
	c.d.mu.Unlock()
	_ = os.RemoveAll(c.dir)
	c.d.busy.Unlock()
}

func (c *capture) started() bool { return c.guid != "" }

// poll — события загрузки: done — файл готов (или ошибка).
func (c *capture) poll(ctx context.Context, max int) (File, bool, error) {
	for {
		var e dlEvent
		select {
		case e = <-c.events:
		default:
			return File{}, false, nil
		}
		if e.method == "Browser.downloadWillBegin" {
			if c.guid == "" {
				c.guid, c.name, c.url = e.GUID, e.Name, e.URL
			}
			continue
		}
		if e.GUID != c.guid {
			continue
		}
		switch {
		case e.Total > float64(max) || e.Got > float64(max):
			return File{}, true, fmt.Errorf("%w: %.0f bytes, the limit is %d", ErrTooLarge, max64(e.Total, e.Got), max)
		case e.State == "canceled":
			c.guid = ""
			return File{}, true, errors.New("download: the download was canceled by the browser or the site")
		case e.State == "completed":
			p := filepath.Join(c.dir, c.guid)
			st, err := os.Stat(p)
			if err != nil {
				return File{}, true, fmt.Errorf("download: the file is not in the downloads folder: %w", err)
			}
			if st.Size() > int64(max) {
				return File{}, true, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, st.Size(), max)
			}
			data, err := os.ReadFile(p)
			c.guid = ""
			if err != nil {
				return File{}, true, err
			}
			return File{Name: c.name, URL: c.url, Via: "download", Data: data}, true, nil
		}
	}
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// UserDownloadDir — папка загрузок пользователя (XDG_DOWNLOAD_DIR на Linux,
// иначе ~/Downloads): туда Chrome без диалога сохраняет обычные загрузки.
func UserDownloadDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "linux" {
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		if f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "XDG_DOWNLOAD_DIR="); ok {
					v = strings.ReplaceAll(strings.Trim(v, `"`), "$HOME", home)
					if filepath.IsAbs(v) && v != home {
						return v
					}
				}
			}
		}
	}
	return filepath.Join(home, "Downloads")
}
