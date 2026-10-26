// SPDX-License-Identifier: AGPL-3.0-only

package browserenv

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// zipOf — архив из файлов (имя → содержимое); бинарник исполняемый.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if strings.HasSuffix(name, "/chrome") || strings.HasSuffix(name, ".exe") {
			h.SetMode(0o755)
		} else {
			h.SetMode(0o644)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	w.Close()
	return b.Bytes()
}

func TestInstallUserChrome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec bits")
	}
	archive := zipOf(t, map[string]string{"chrome-linux64/chrome": "#!/bin/sh\necho Google Chrome 131.0.1\n", "chrome-linux64/libEGL.so": "lib", "chrome-linux64/resources/x.pak": "pak"})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/versions.json":
			w.Write([]byte(`{"channels":{"Stable":{"version":"131.0.1","downloads":{"chrome":[{"platform":"linux64","url":"` + srv.URL + `/chrome-linux64.zip"},{"platform":"mac-arm64","url":"` + srv.URL + `/mac.zip"}]}}}}`))
		case "/chrome-linux64.zip":
			w.Write(archive)
		case "/evil.zip":
			w.Write(zipOf(t, map[string]string{"../../escape": "x", "chrome-linux64/chrome": "y"}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldURL, oldPre := CFTVersionsURL, cftDownloadPrefixes
	CFTVersionsURL, cftDownloadPrefixes = srv.URL+"/versions.json", append(cftDownloadPrefixes, srv.URL+"/")
	defer func() { CFTVersionsURL, cftDownloadPrefixes = oldURL, oldPre }()
	data := t.TempDir()
	s := System()
	s.GOOS, s.GOARCH, s.DataDir = "linux", "amd64", data
	var out bytes.Buffer
	path, err := InstallUserChrome(context.Background(), s, data, &out)
	if err != nil || path != filepath.Join(data, "chrome", "131.0.1", "chrome-linux64", "chrome") {
		t.Fatal(path, err, out.String())
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatal("binary not executable", st, err)
	}
	if !strings.Contains(out.String(), "Terms of Service") || !strings.Contains(out.String(), "Downloading Chrome for Testing 131.0.1 (linux64)") {
		t.Fatal(out.String())
	}
	if m, _ := filepath.Glob(filepath.Join(data, "chrome", "download-*.zip")); len(m) != 0 {
		t.Fatal("temp archive left", m)
	}
	// Поставленный Chrome находит проверка — без PATH и без root.
	s.Getenv = func(string) string { return "" }
	s.Output = func(name string, args ...string) (string, error) {
		if name == "ldd" {
			return "\tlibnss3.so => not found\n", nil
		}
		return "Google Chrome 131.0.1", nil
	}
	s.ReadFile = func(p string) ([]byte, error) {
		if p == "/etc/os-release" {
			return []byte("ID=debian\n"), nil
		}
		return os.ReadFile(p)
	}
	s.Root = false
	c := CheckBrowser(s)
	if c.Chrome != path || c.OK || strings.Join(c.Libraries, ",") != "libnss3.so" || !strings.Contains(c.Summary, "To run Chrome, install: libnss3.") {
		t.Fatalf("%+v", c)
	}
	// Повторный вызов: уже есть, ничего не скачивается.
	out.Reset()
	if p2, err := InstallUserChrome(context.Background(), s, data, &out); err != nil || p2 != path || !strings.Contains(out.String(), "already in") {
		t.Fatal(p2, err, out.String())
	}
	// Нет сборки под платформу — понятная ошибка; архив с выходом за каталог — отказ.
	s.GOARCH = "arm64"
	if _, err := InstallUserChrome(context.Background(), s, t.TempDir(), &out); err == nil || !strings.Contains(err.Error(), "no build for linux/arm64") {
		t.Fatal(err)
	}
	if err := unzipInto(writeTemp(t, zipOf(t, map[string]string{"../../escape": "x"})), filepath.Join(t.TempDir(), "v")); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatal(err)
	}
	// Chrome нет, платформа есть: заметка про --user.
	s.GOARCH, s.DataDir = "amd64", filepath.Join(data, "other")
	s.Glob = func(string) []string { return nil }
	s.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if c := CheckBrowser(s); c.Chrome != "" || !strings.Contains(strings.Join(c.Notes, " "), "gate4ai browser setup --user downloads Chrome for Testing into "+filepath.Join(data, "other", "chrome")) {
		t.Fatalf("%+v", c)
	}
}

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	os.WriteFile(p, b, 0o600)
	return p
}
