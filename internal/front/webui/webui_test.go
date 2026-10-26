// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"bytes"
	"context"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/tray"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

type notes struct{ n int }

func (x *notes) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "notes", Actions: []manifest.Action{
		{Name: "append", Class: "write", InputSchema: manifest.MustSchema(`{ type: object, properties: { text: { type: string } }, required: [text] }`)}}}
}
func (x *notes) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	x.n++
	return abi.ExecResult{Structured: "ok"}, nil
}
func (x *notes) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

const cfg = "agents:\n  bot: {profile: p}\nprofiles:\n  p:\n    connectors: [notes]\n"

func setup(t *testing.T, extra ...connectors.Connector) (*UI, *daemon.Daemon, *notes, string) {
	return setupCfg(t, cfg, extra...)
}

// setupCfg — Web UI с начальной конфигурацией conf и коннекторами notes и extra.
func setupCfg(t *testing.T, conf string, extra ...connectors.Connector) (*UI, *daemon.Daemon, *notes, string) {
	home, _ := os.MkdirTemp("", "g4aui")
	t.Cleanup(func() { os.RemoveAll(home) })
	n := &notes{}
	c, _ := connectors.NewInProcess(n)
	cs := []connectors.Client{c}
	for _, x := range extra {
		xc, err := connectors.NewInProcess(x)
		if err != nil {
			t.Fatal(err)
		}
		cs = append(cs, xc)
	}
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: cs, InitialConfig: []byte(conf)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	u := New(d)
	// Тексты в проверках — русский перевод (основной язык интерфейса — английский, его проверяет TestEnglishByDefault).
	u.DefaultLang = langRU
	l, err := u.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: u.Handler()}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return u, d, n, "http://" + u.Addr
}

func body(t *testing.T, r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

var refreshRe = regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`)

// metaRefresh — транспорт тестового клиента: как браузер, идёт по
// <meta http-equiv="refresh"> (так ссылка входа уводит в UI, sameSiteRedirect).
type metaRefresh struct{}

func (metaRefresh) RoundTrip(req *http.Request) (*http.Response, error) {
	r, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		return r, err
	}
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	if m := refreshRe.FindSubmatch(b); m != nil {
		r.StatusCode, r.Status = http.StatusSeeOther, "303 See Other"
		r.Header.Set("Location", html.UnescapeString(string(m[1])))
	}
	return r, nil
}

func TestWebUI(t *testing.T) {
	u, d, n, base := setup(t)
	if _, err := u.Listen("0.0.0.0:0"); err == nil {
		t.Fatal("must refuse non-loopback")
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Transport: metaRefresh{}}

	r, _ := c.Get(base + "/")
	// Пароля ещё нет (до EnsurePassword): форма пароля, но войти нельзя ничем.
	if r.StatusCode != 401 || !strings.Contains(body(t, r), `name="start"`) {
		t.Fatal("must ask for the password")
	}
	if r, _ := c.PostForm(base+"/login", url.Values{"start": {"1"}, "password": {""}}); r.StatusCode != 401 || strings.Contains(body(t, r), `action="/logout"`) {
		t.Fatal("sign-in before the password exists", r.StatusCode)
	}
	r = signIn(t, u, c, base)
	page := body(t, r)
	csrf := csrfRe.FindStringSubmatch(page)[1]
	// Без сессии — форма пароля; с чужим паролем — отказ.
	c2 := &http.Client{Transport: metaRefresh{}}
	if r, _ := c2.PostForm(base+"/login", url.Values{"start": {"1"}, "password": {"wrong password"}}); r.StatusCode != 401 {
		t.Fatal("wrong password")
	}
	// DNS rebinding: чужой Host.
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.Host = "evil.example:80"
	if r, _ := c.Do(req); r.StatusCode != http.StatusMisdirectedRequest {
		t.Fatal("foreign host accepted", r.StatusCode)
	}

	// Подтверждение из Web UI.
	res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "hello"}})
	r, _ = c.Get(base + "/")
	if p := body(t, r); !strings.Contains(p, "bot") || !strings.Contains(p, "+ hello") {
		t.Fatal("pending not shown")
	}
	if r, _ := c.PostForm(base+"/approvals/"+res.ID, url.Values{"decision": {"approve"}}); r.StatusCode != 403 {
		t.Fatal("missing csrf accepted")
	}
	req, _ = http.NewRequest("POST", base+"/approvals/"+res.ID, strings.NewReader(url.Values{"decision": {"approve"}, "csrf": {csrf}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := c.Do(req); r.StatusCode != 403 {
		t.Fatal("cross-origin accepted")
	}
	r, _ = c.PostForm(base+"/approvals/"+res.ID, url.Values{"decision": {"approve"}, "csrf": {csrf}})
	if p := body(t, r); n.n != 1 || !strings.Contains(p, "Разрешено: bot → notes.append, итог: выполнено") || !strings.Contains(p, "<summary>Подробности</summary><code>#") {
		t.Fatal("approve failed", n.n)
	}
	// Агенты: токен показывается один раз, код подключения, отзыв.
	r, _ = c.PostForm(base+"/agents/bot/token", url.Values{"csrf": {csrf}, "label": {"cli"}})
	p := body(t, r)
	tok := regexp.MustCompile(`g4a_[A-Za-z0-9_-]+`).FindString(p)
	if tok == "" {
		t.Fatal("token not shown")
	}
	if a, err := d.State.AgentByToken(tok); err != nil || a != "bot" {
		t.Fatal("token invalid")
	}
	r, _ = c.PostForm(base+"/agents/bot/connect", url.Values{"csrf": {csrf}})
	if !regexp.MustCompile(`[A-Z2-9]{4}-[A-Z2-9]{4}`).MatchString(body(t, r)) {
		t.Fatal("no connect code")
	}
	c.PostForm(base+"/agents/bot/revoke", url.Values{"csrf": {csrf}})
	if _, err := d.State.AgentByToken(tok); err == nil {
		t.Fatal("not revoked")
	}
	// Журнал и квитанция.
	r, _ = c.Get(base + "/journal")
	p = body(t, r)
	if !strings.Contains(p, "Журнал не изменён") || !strings.Contains(p, "notes.append") {
		t.Fatal("journal")
	}
	hash := regexp.MustCompile(`/journal/([0-9a-f]{64})`).FindStringSubmatch(p)[1]
	r, _ = c.Get(base + "/journal/" + hash)
	if !strings.Contains(body(t, r), `&#34;prev_hash&#34;`) {
		t.Fatal("receipt page")
	}
	// Политика: ошибка схемы со строкой, сохранение, симулятор, откат.
	r, _ = c.PostForm(base+"/policy", url.Values{"csrf": {csrf}, "yaml": {cfg + "    rules: [{id: r, action: notes.append, effect: maybe}]\n"}})
	if p := body(t, r); !strings.Contains(p, "line 6") || !strings.Contains(p, "unknown effect") {
		t.Fatal("schema error not shown", p)
	}
	r, _ = c.PostForm(base+"/policy", url.Values{"csrf": {csrf}, "yaml": {cfg + "    rules: [{id: r, action: notes.append, effect: allow}]\n"}, "comment": {"allow notes"}})
	if !strings.Contains(body(t, r), "Сохранено как версия 2") {
		t.Fatal("save")
	}
	r, _ = c.PostForm(base+"/policy/simulate", url.Values{"csrf": {csrf}, "agent": {"bot"}, "action": {"notes.append"}, "arg.text": {"x"}})
	if p := body(t, r); !strings.Contains(p, `class="allow">разрешать`) {
		t.Fatal("simulate", p)
	}
	r, _ = c.PostForm(base+"/policy/rollback", url.Values{"csrf": {csrf}, "version": {"1"}})
	if !strings.Contains(body(t, r), "Возвращена версия 1") {
		t.Fatal("rollback")
	}
	// Секреты: значение не отображается.
	r, _ = c.PostForm(base+"/vault", url.Values{"csrf": {csrf}, "alias": {"@mail.work"}, "value": {"S3CRET-VALUE"}})
	if p := body(t, r); !strings.Contains(p, "@mail.work") || strings.Contains(p, "S3CRET") || !strings.Contains(p, "не используется") {
		t.Fatal("vault page")
	}
	// Выход.
	c.PostForm(base+"/logout", url.Values{"csrf": {csrf}})
	if r, _ := c.Get(base + "/agents"); r.StatusCode != 401 {
		t.Fatal("session survives logout")
	}
}

func TestOwnerRoutesOnSocket(t *testing.T) {
	u, d, _, _ := setup(t)
	d.AddRoutes(u.OwnerRoutes)
	home := t.TempDir()
	sock, asock := filepath.Join(home, "o.sock"), filepath.Join(home, "a.sock")
	if len(sock) > 100 {
		t.Skip("socket path too long")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, sock, asock)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(asock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	owner := ipc.HTTPClient(sock)
	pw, _ := u.EnsurePassword()
	r, err := owner.Post("http://gate4ai/v1/ui/url", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Сгенерированный пароль — владельцу вместе с адресом, пока им не вошли.
	if p := body(t, r); r.StatusCode != 200 || !strings.Contains(p, `"url":"http://`+u.Addr+`/"`) || !strings.Contains(p, `"password":"`+pw+`"`) {
		t.Fatal(r.StatusCode, p)
	}
	d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "x"}})
	r, _ = owner.Get("http://gate4ai/v1/tray/state")
	if p := body(t, r); !strings.Contains(p, `"pending":[{`) || !strings.Contains(p, `"agent":"bot"`) || !strings.Contains(p, `"ui_password":"`+pw+`"`) {
		t.Fatal(p)
	}
	// Сброс защиты — новый пароль; агенту с токеном недоступен.
	r, _ = owner.Post("http://gate4ai/v1/ui/reset-login", "application/json", nil)
	if p := body(t, r); r.StatusCode != 200 || strings.Contains(p, pw) || !strings.Contains(p, `"password":"`) {
		t.Fatal(r.StatusCode, p)
	}
	// Агент с токеном адрес Web UI не получает.
	tok, _ := d.State.IssueToken("bot", "", "")
	req, _ := http.NewRequest("POST", "http://gate4ai/v1/ui/url", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if r, _ := ipc.HTTPClient(asock).Do(req); r.StatusCode == 200 {
		t.Fatal("agent got the Web UI address")
	}
	req, _ = http.NewRequest("POST", "http://gate4ai/v1/ui/reset-login", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if r, _ := ipc.HTTPClient(asock).Do(req); r.StatusCode == 200 {
		t.Fatal("agent reset the Web UI login")
	}
	if r, _ := ipc.HTTPClient(sock).Do(req); r.StatusCode != 403 {
		t.Fatal("agent token on owner socket got the Web UI address", r.StatusCode)
	}
}

// Контракт трея: запросы, которые делает cmd/gate4ai-tray через пакет tray.
func TestTrayContract(t *testing.T) {
	u, d, n, _ := setup(t)
	d.AddRoutes(u.OwnerRoutes)
	home := t.TempDir()
	sock, asock := filepath.Join(home, "o.sock"), filepath.Join(home, "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, sock, asock)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(asock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tr := tray.New(cli.NewSocketClient(sock, ""))
	res := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "x"}})
	st := tr.Fetch(ctx)
	if st == nil || len(st.Pending) != 1 {
		t.Fatalf("%+v", st)
	}
	p := st.Pending[0]
	if p.ID != res.ID || p.Agent != "bot" || p.Action != "notes.append" || !strings.HasPrefix(p.Hash, "#") || p.Summary == "" || p.Reason == "" {
		t.Fatalf("%+v", p)
	}
	if p.WebOnly || p.NeedsWebUI() {
		t.Fatal("an ordinary action is decided in the tray")
	}
	if f := tr.Fresh(st); len(f) != 1 || len(tr.Fresh(st)) != 0 {
		t.Fatal("each request must be notified once")
	}
	if err := tr.Decide(ctx, p.ID, true); err != nil || n.n != 1 {
		t.Fatal(err, n.n)
	}
	if st := tr.Fetch(ctx); st == nil || len(st.Pending) != 0 || tray.MenuKey(st) != "ok,b0/0,p0" || tray.BrowserLine(st) != "" || tray.PasswordLine(st) != "" {
		t.Fatalf("%+v", st)
	}
	if l, err := tr.URL(ctx); err != nil || l != "http://"+u.Addr+"/" {
		t.Fatal(l, err)
	}
	if l, err := tr.URLTo(ctx, "/browser"); err != nil || l != "http://"+u.Addr+"/browser" {
		t.Fatal(l, err)
	}
	// Агентский сокет трею ничего не отдаёт.
	if tray.New(cli.NewSocketClient(asock, "")).Fetch(ctx) != nil {
		t.Fatal("tray state on the agent socket")
	}
}

func TestAccountsAndQuestions(t *testing.T) {
	u, d, _, base := setup(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Transport: metaRefresh{}}
	r := signIn(t, u, c, base)
	csrf := csrfRe.FindStringSubmatch(body(t, r))[1]
	acc := cfg + `credentials:
  "@site":
    bind: ["https://login.example"]
    form:
      fields: [{ selector: "#login", value: "me" }, { selector: "#password", secret: "@site.password" }]
      submit: { selector: "#go" }
    use: [p]
`
	r, _ = c.PostForm(base+"/policy", url.Values{"csrf": {csrf}, "yaml": {acc}, "comment": {"account"}})
	if p := body(t, r); !strings.Contains(p, "Сохранено как версия 2") {
		t.Fatal("save", p)
	}
	r, _ = c.Get(base + "/vault")
	p := body(t, r)
	for _, want := range []string{"Учётки для входа на сайты", "<code>@site</code>", "https://login.example", "пароль <code>@site.password</code>", "нет @site.password", `name="alias" value="@site.password"`, "Задать", "логин «me»"} {
		if !strings.Contains(p, want) {
			t.Errorf("vault page: no %q", want)
		}
	}
	got := make(chan string, 1)
	go func() {
		v, _ := d.Pipeline.Ask(context.Background(), pipeline.Question{Agent: "bot", Account: "@site", Bind: []string{"https://login.example"}, Text: "Код из SMS"})
		got <- v
	}()
	var qs []daemon.Question
	for i := 0; i < 100 && len(qs) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		qs = d.Questions()
	}
	r, _ = c.Get(base + "/approvals")
	if p := body(t, r); !strings.Contains(p, "Нужен код") || !strings.Contains(p, "Код из SMS") {
		t.Fatal("approvals page", p)
	}
	r, _ = c.PostForm(base+"/questions/"+qs[0].ID, url.Values{"csrf": {csrf}, "value": {"778899"}, "decision": {"answer"}})
	if p := body(t, r); !strings.Contains(p, "Код введён") || strings.Contains(p, "778899") {
		t.Fatal("answer", p)
	}
	if v := <-got; v != "778899" {
		t.Fatal(v)
	}
}

// Живая очередь подтверждений и гонка с другим каналом.
func TestLiveApprovals(t *testing.T) {
	u, d, _, base := setup(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Transport: metaRefresh{}}
	if r, _ := c.Get(base + "/approvals/live"); r.StatusCode != 401 {
		t.Fatal("live without session", r.StatusCode)
	}
	r := signIn(t, u, c, base)
	csrf := csrfRe.FindStringSubmatch(body(t, r))[1]
	live := func(known ...string) string {
		r, _ := c.Get(base + "/approvals/live?back=/approvals&known=" + url.QueryEscape(strings.Join(known, ",")))
		if r.StatusCode != 200 {
			t.Fatal("live", r.StatusCode)
		}
		return body(t, r)
	}
	if p := live(); !strings.Contains(p, `"pending":[]`) || !strings.Contains(p, `"count":0`) {
		t.Fatal("empty queue", p)
	}
	// Счётчик для шапки на страницах без очереди: только число, без карточек.
	count := func() string {
		r, _ := c.Get(base + "/approvals/live?count=1")
		if r.StatusCode != 200 {
			t.Fatal("live count", r.StatusCode)
		}
		return strings.TrimSpace(body(t, r))
	}
	if p := count(); p != `{"count":0}` {
		t.Fatal("count", p)
	}
	// Новый запрос появляется карточкой с кнопками.
	a := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "one"}})
	p := live()
	if !strings.Contains(p, `"id":"`+a.ID+`"`) || !strings.Contains(p, `data-approval=\"`+a.ID+`\"`) || !strings.Contains(p, "Разрешить") || !strings.Contains(p, `"count":1`) {
		t.Fatal("new approval not live", p)
	}
	if p := count(); p != `{"count":1}` {
		t.Fatal("count", p)
	}
	// Решён в Telegram — уходит из очереди с пояснением.
	if _, err := d.Pipeline.Decide(context.Background(), a.ID, true, "telegram", "tg:1"); err != nil {
		t.Fatal(err)
	}
	p = live(a.ID)
	if strings.Contains(p, `data-approval`) || !strings.Contains(p, "разрешено через Telegram") {
		t.Fatal("decided approval stays live", p)
	}
	// Нажатие на устаревшей странице — понятное сообщение, а не сырая ошибка.
	r, _ = c.PostForm(base+"/approvals/"+a.ID, url.Values{"decision": {"reject"}, "csrf": {csrf}})
	p = body(t, r)
	if strings.Contains(p, "already decided") || !strings.Contains(p, "Уже решено") || !strings.Contains(p, "разрешено через Telegram") {
		t.Fatal("race with another channel", p)
	}
	// Повторная отправка той же формы.
	b := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "two"}})
	c.PostForm(base+"/approvals/"+b.ID, url.Values{"decision": {"reject"}, "csrf": {csrf}})
	r, _ = c.PostForm(base+"/approvals/"+b.ID, url.Values{"decision": {"reject"}, "csrf": {csrf}})
	if p := body(t, r); strings.Contains(p, "already decided") || !strings.Contains(p, "отклонено через Web UI") {
		t.Fatal("double submit", p)
	}
	// Истёк срок ожидания.
	e := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "three"}})
	d.State.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	d.Pipeline.ExpireDue(context.Background())
	if p := live(e.ID); !strings.Contains(p, "никто не ответил вовремя") {
		t.Fatal("timeout not explained", p)
	}
	if p := live("missing"); !strings.Contains(p, "больше не ждёт решения") {
		t.Fatal("unknown id", p)
	}
}
