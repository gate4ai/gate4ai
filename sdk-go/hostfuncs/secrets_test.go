// SPDX-License-Identifier: Apache-2.0

package hostfuncs

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

const apiCaps = `capabilities:
  network: [ { host: 127.0.0.1, scheme: http } ]
  secrets: [ { alias: "@api", bind: [127.0.0.1] } ]
`

// Коннектор не может обойти маскирование ответа: сжатием (Accept-Encoding),
// частичным ответом (Range) или экранированием значения в ответе.
func TestHTTPMaskBypass(t *testing.T) {
	var gotAE, gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAE, gotRange = r.Header.Get("Accept-Encoding"), r.Header.Get("Range")
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/gzip-unasked": // сервер сжимает, даже если не просили
			w.Header().Set("Content-Encoding", "br")
			io.WriteString(w, "compressed "+auth)
			return
		case "/json":
			b, _ := json.Marshal(map[string]string{"echo": auth})
			w.Write(b)
			return
		case "/b64":
			io.WriteString(w, base64.StdEncoding.EncodeToString([]byte(auth)))
			return
		}
		if strings.Contains(gotAE, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			io.WriteString(gz, "echo: "+auth)
			gz.Close()
			return
		}
		io.WriteString(w, "echo: "+auth)
	}))
	defer srv.Close()
	d := dispatcher(t, apiCaps, nil)
	d.Secrets = secrets{"@api": "SUPERSECRETVALUE/1&2"}
	hdr := map[string]string{"Authorization": "Bearer {{secret:@api}}", "Accept-Encoding": "gzip", "Range": "bytes=10-20"}
	var resp abi.HTTPResponse
	if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL + "/", Headers: hdr}, &resp); err != nil {
		t.Fatal(err)
	}
	if gotRange != "" {
		t.Fatal("Range from the connector reached the server")
	}
	// Хост сам согласовал gzip и распаковал ответ до маскирования.
	if resp.Headers["Content-Encoding"] != "" || !strings.Contains(string(resp.Body), "echo: Bearer [secret]") {
		t.Fatalf("%q %v", resp.Body, resp.Headers)
	}
	for _, path := range []string{"/json", "/b64"} {
		if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL + path, Headers: hdr}, &resp); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(resp.Body), "SUPERSECRET") || strings.Contains(string(resp.Body), "U1VQRVJTRUNSRVRWQUxVRS") {
			t.Fatalf("%s: %q", path, resp.Body)
		}
	}
	if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL + "/gzip-unasked", Headers: hdr}, &resp); code(err) != abi.CodeIO {
		t.Fatal("unknown Content-Encoding in a call with secrets:", err)
	}
}

// Секрет подставляется только целиком в заголовок аутентификации.
func TestHTTPSecretPlacement(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()
	d := dispatcher(t, apiCaps, nil)
	var resp abi.HTTPResponse
	for _, h := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL, Headers: map[string]string{h: "Token {{secret:@api}}"}}, &resp); err != nil {
			t.Fatal(h, err)
		}
		if got.Get(h) != "Token tok-SECRET-123" {
			t.Fatal(h, got.Get(h))
		}
	}
	for _, r := range []abi.HTTPRequest{
		{URL: srv.URL, Body: []byte(`{"q":"{{secret:@api}}"}`)},
		{URL: srv.URL, Headers: map[string]string{"User-Agent": "{{secret:@api}}"}},
		{URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer x {{secret:@api}} y"}},
		{URL: srv.URL, Headers: map[string]string{"Authorization": "{{secret:@api}}{{secret:@api}}"}},
	} {
		if err := call(t, d, abi.FnHTTPRequest, r, &resp); code(err) != abi.CodeBadRequest {
			t.Errorf("%+v: %v", r, err)
		}
	}
}

// Текстовые доказательства маскируются, изображения — нет.
func TestEvidenceMasked(t *testing.T) {
	d := dispatcher(t, apiCaps, nil)
	d.used = []string{"tok-SECRET-123"}
	call(t, d, abi.FnEvidenceAdd, abi.EvidenceItem{Name: "a.txt", MIME: "text/plain", Data: []byte("key tok-SECRET-123")}, nil)
	call(t, d, abi.FnEvidenceAdd, abi.EvidenceItem{Name: "a.png", MIME: "image/png", Data: []byte("\x89PNG tok-SECRET-123")}, nil)
	ev := d.Evidence()
	if len(ev) != 2 || strings.Contains(string(ev[0].Data), "tok-SECRET") || !strings.Contains(string(ev[1].Data), "tok-SECRET") {
		t.Fatal(ev)
	}
}

// Модуль не читает соединение — хост не копит данные без предела.
func TestConnBufferLimit(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	sent := make(chan int64, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		n, _ := io.Copy(c, io.LimitReader(zeros{}, 4*MaxConnBuffer))
		sent <- n
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cn := newConn(c, nil)
	go cn.pump()
	n := <-sent
	cn.mu.Lock()
	buffered := len(cn.buf)
	cn.mu.Unlock()
	if buffered > MaxConnBuffer+64<<10 || n >= 4*MaxConnBuffer {
		t.Fatalf("buffered %d, sent %d", buffered, n)
	}
	cn.close()
}

type zeros struct{}

func (zeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }

// Подмена каталога симлинком после проверки пути не выводит запись за
// пределы разрешённого корня (TOCTOU).
func TestFSSymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "a"), 0o755)
	d := dispatcher(t, `capabilities: { files: ["${setting:root}/**"] }`, map[string]any{"root": ws})
	p := filepath.Join(ws, "a", "authorized_keys")
	loc, err := d.Check.Locate(p)
	if err != nil {
		t.Fatal(err)
	}
	// Гонка: после проверки ws/a заменён симлинком наружу.
	os.RemoveAll(filepath.Join(ws, "a"))
	if err := os.Symlink(outside, filepath.Join(ws, "a")); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{abi.FnFSWrite, abi.FnFSAppend} {
		if _, err := d.fsAt(fn, abi.FSRequest{Path: p, Data: []byte("ssh-ed25519 attacker")}, loc); err == nil {
			t.Errorf("%s: write through a swapped symlink", fn)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("file written outside the root:", ents[0].Name())
	}
	// Правило на один файл: подмена самого файла симлинком.
	one := filepath.Join(ws, "todo.md")
	os.WriteFile(one, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(ws, "other"), []byte("keep"), 0o644)
	d1 := dispatcher(t, `capabilities: { files: ["`+one+`"] }`, nil)
	loc, err = d1.Check.Locate(one)
	if err != nil || !loc.Exact {
		t.Fatal(loc, err)
	}
	os.Remove(one)
	os.Symlink("other", one)
	if _, err := d1.fsAt(abi.FnFSAppend, abi.FSRequest{Path: one, Data: []byte("bad")}, loc); err == nil {
		t.Fatal("append through a swapped file symlink")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "other")); string(b) != "keep" {
		t.Fatal(string(b))
	}
	// Без подмены всё работает.
	os.Remove(one)
	if _, err := d1.fs(abi.FnFSAppend, abi.FSRequest{Path: one, Data: []byte("ok")}); err != nil {
		t.Fatal(err)
	}
}
