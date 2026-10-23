// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// pdfBytes — «PDF» с двоичными байтами: файл должен прийти байт в байт.
var pdfBytes = append([]byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n"), append(bytes.Repeat([]byte{0, 0xff, 0x80, 'x'}, 5000), "\n%%EOF\n"...)...)

// downloadSite — кнопки, которые скачивают файл по-разному: blob (как
// FileSaver), data: URL, fetch с заголовком страницы и показ во вкладке
// (без загрузки), большой файл и кнопка без загрузки.
type downloadSite struct{}

const downloadPage = `<!doctype html><title>Docs</title>
<button id="blob">blob</button><button id="data">data</button><button id="view">view</button>
<button id="big">big</button><button id="none">none</button><button id="link">link</button>
<script>
const jwt = 'page-secret-jwt';
async function doc() { return (await fetch('/api/document?id=42', {headers: {'X-Access-JWT': jwt}})).blob(); }
function save(href, name) { const a = document.createElement('a'); a.href = href; a.download = name; document.body.appendChild(a); a.click(); a.remove(); }
document.getElementById('blob').onclick = async () => save(URL.createObjectURL(await doc()), 'Анализ мочи');
document.getElementById('data').onclick = async () => { const r = new FileReader(); r.onload = () => save(r.result, 'scan.pdf'); r.readAsDataURL(await doc()); };
document.getElementById('view').onclick = async () => { await doc(); };
document.getElementById('big').onclick = () => save('/big.bin', 'big.bin');
document.getElementById('link').onclick = () => save('/file.pdf', '');
</script>`

func (downloadSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(downloadPage))
	case "/api/document":
		if r.Header.Get("X-Access-JWT") != "page-secret-jwt" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"X-Access-JWT header is not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(pdfBytes)
	case "/file.pdf":
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="result 42.pdf"`)
		w.Write(pdfBytes)
	case "/big.bin":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(make([]byte, MaxDownloadBytes+1024))
	default:
		http.NotFound(w, r)
	}
}

// chromeService — воркер с настоящим Chrome (headless) и страницей сайта h:
// сессия, адрес сайта и папка обычных загрузок.
func chromeService(t *testing.T, h http.Handler) (*Session, string, string, context.Context) {
	t.Helper()
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	svc := New()
	t.Cleanup(func() { svc.Close() })
	user := filepath.Join(dir, "Downloads")
	s, err := svc.Acquire(ctx, Settings{Engine: "cdp", ChromePath: wrapper, ProfileDir: filepath.Join(dir, "profile"), DownloadDir: user}, 0)
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	t.Cleanup(s.Release)
	if _, err := s.Open(ctx, srv.URL+"/", domain.Sites{Domains: []string{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	return s, srv.URL, user, ctx
}

func TestDownloadRealChrome(t *testing.T) {
	s, base, user, ctx := chromeService(t, downloadSite{})
	sum := sha256.Sum256(pdfBytes)
	want := hex.EncodeToString(sum[:])
	check := func(name string, f File, err error, fname, via string) {
		t.Helper()
		if err != nil {
			t.Fatal(name, err)
		}
		if !bytes.Equal(f.Data, pdfBytes) || f.SHA256 != want || f.MIME != "application/pdf" || f.Name != fname || f.Via != via {
			t.Fatalf("%s: %q %s %s %s, %d bytes", name, f.Name, f.MIME, f.Via, f.SHA256, len(f.Data))
		}
	}
	// Загрузка Chrome из blob (FileSaver): без диалога, имя — с расширением по типу.
	f, err := s.Download(ctx, DownloadRequest{Selector: "#blob"})
	check("blob", f, err, "Анализ мочи.pdf", "download")
	if f.URL != "" {
		t.Fatal("a blob: address is not shown:", f.URL)
	}
	// data: URL (FileReader → <a download>).
	f, err = s.Download(ctx, DownloadRequest{Selector: "#data"})
	check("data", f, err, "scan.pdf", "download")
	// Ссылка на файл сайта: адрес и имя из Content-Disposition.
	f, err = s.Download(ctx, DownloadRequest{Selector: "#link"})
	check("link", f, err, "result 42.pdf", "download")
	if f.URL != base+"/file.pdf" {
		t.Fatal(f.URL)
	}
	// Временная папка удалена, в папке пользователя ничего не осталось.
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "gate4ai-download-*")); len(left) > 0 {
		for _, l := range left {
			if st, err := os.Stat(l); err == nil && time.Since(st.ModTime()) < time.Minute {
				t.Fatal("temporary folder left:", l)
			}
		}
	}
	if es, _ := os.ReadDir(user); len(es) > 0 {
		t.Fatal("a file in the user's folder:", es[0].Name())
	}

	// Страница показывает файл, а не скачивает: тело её ответа (со своим
	// заголовком X-Access-JWT, которого агент не видит).
	f, err = s.Download(ctx, DownloadRequest{Selector: "#view", ResponseURLContains: "/api/document"})
	check("view", f, err, "download.pdf", "response")
	if !strings.HasPrefix(f.URL, base+"/api/document") {
		t.Fatal(f.URL)
	}
	// Тот же адрес запросом без заголовка страницы — 401 с подсказкой.
	if _, err := s.Download(ctx, DownloadRequest{URL: base + "/api/document?id=42"}); err == nil || !strings.Contains(err.Error(), "answered 401") {
		t.Fatal(err)
	}
	// Запрос со страницы (её cookies).
	f, err = s.Download(ctx, DownloadRequest{URL: base + "/file.pdf"})
	check("url", f, err, "result 42.pdf", "fetch")

	// Пределы: размер, тип, клик без загрузки, сайты.
	if _, err := s.Download(ctx, DownloadRequest{Selector: "#big"}); !errors.Is(err, ErrTooLarge) {
		t.Fatal("size:", err)
	}
	if _, err := s.Download(ctx, DownloadRequest{URL: base + "/big.bin"}); !errors.Is(err, ErrTooLarge) {
		t.Fatal("size of a request:", err)
	}
	if _, err := s.Download(ctx, DownloadRequest{Selector: "#blob", MaxBytes: 1000}); !errors.Is(err, ErrTooLarge) {
		t.Fatal("owner's size:", err)
	}
	if _, err := s.Download(ctx, DownloadRequest{Selector: "#blob", Types: []string{"image/*", ".csv"}}); !errors.Is(err, ErrFileType) {
		t.Fatal("type:", err)
	}
	if f, err := s.Download(ctx, DownloadRequest{Selector: "#blob", Types: []string{".pdf"}}); err != nil || f.Name != "Анализ мочи.pdf" {
		t.Fatal("allowed type:", err)
	}
	if _, err := s.Download(ctx, DownloadRequest{Selector: "#none", TimeoutMS: 1000}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "response_url_contains") {
		t.Fatal("no download:", err)
	}
	if _, err := s.Download(ctx, DownloadRequest{URL: "https://evil.example/file.pdf"}); !errors.Is(err, ErrDomain) {
		t.Fatal("site list:", err)
	}

	// Клик агента (act) по «Скачать» вне download: файл молча ложится в папку
	// пользователя, диалога и зависания нет — страница отвечает.
	if _, err := s.Act(ctx, ActRequest{Selector: "#link", Method: "click"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, err := os.ReadFile(filepath.Join(user, "result 42.pdf")); err == nil && bytes.Equal(got, pdfBytes) {
			break
		}
		if time.Now().After(deadline) {
			es, _ := os.ReadDir(user)
			t.Fatal("the download did not land in the user's folder:", es)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := s.Observe(ctx, 0); err != nil {
		t.Fatal("the page is stuck:", err)
	}
}

func TestCheckDownload(t *testing.T) {
	for _, r := range []DownloadRequest{
		{},
		{Selector: "#a", URL: "https://a.example/f"},
		{URL: "https://a.example/f", ResponseURLContains: "/api"},
		{Selector: "#a", TimeoutMS: 200000},
	} {
		if CheckDownload(r) == nil {
			t.Fatalf("%+v accepted", r)
		}
	}
	if err := CheckDownload(DownloadRequest{Selector: "#a", ResponseURLContains: "/api", TimeoutMS: 120000}); err != nil {
		t.Fatal(err)
	}
}

func TestFinishNamesAndTypes(t *testing.T) {
	for _, c := range []struct{ name, mime, data, wantName, wantMIME string }{
		{"Клинический анализ мочи", "", "%PDF-1.4", "Клинический анализ мочи.pdf", "application/pdf"},
		{"../../etc/passwd", "text/plain; charset=utf-8", "x", "_.._etc_passwd", "text/plain"},
		{"", "application/octet-stream", "%PDF-1.7", "download.pdf", "application/pdf"},
		{"report.csv", "", "a,b", "report.csv", "text/csv"},
	} {
		f, err := finish(File{Name: c.name, MIME: c.mime, Data: []byte(c.data), URL: "blob:http://x/1"}, DownloadRequest{})
		if err != nil || f.Name != c.wantName || f.MIME != c.wantMIME || f.URL != "" || len(f.SHA256) != 64 {
			t.Fatalf("%q: %q %q %q %v", c.name, f.Name, f.MIME, f.URL, err)
		}
	}
	if _, err := finish(File{Name: "a.exe", Data: []byte("MZ")}, DownloadRequest{Types: []string{"application/pdf"}}); !errors.Is(err, ErrFileType) {
		t.Fatal(err)
	}
}
