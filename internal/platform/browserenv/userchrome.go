// SPDX-License-Identifier: AGPL-3.0-only

package browserenv

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Chrome for Testing — сборки Chrome от Google без установщика: zip, который
// распаковывается в каталог данных gate4ai и не требует root (так ставят Chrome
// Puppeteer и Playwright). Системные библиотеки он не приносит: после установки
// CheckBrowser называет недостающие (ldd) пакетами дистрибутива.

// CFTVersionsURL — список последних версий Chrome for Testing со ссылками
// (переменная — для тестов).
var CFTVersionsURL = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"

// cftDownloadPrefixes — откуда разрешено скачивать архив (тесты добавляют свой сервер).
var cftDownloadPrefixes = []string{"https://storage.googleapis.com/", "https://googlechromelabs.github.io/"}

// UserChromeDir — куда ставится Chrome for Testing: <каталог данных>/chrome.
func UserChromeDir(dataDir string) string { return filepath.Join(dataDir, "chrome") }

// cftPlatform — платформа Chrome for Testing для GOOS/GOARCH ("" — сборки нет:
// Linux arm64).
func cftPlatform(goos, goarch string) string {
	return map[string]string{"linux/amd64": "linux64", "darwin/arm64": "mac-arm64", "darwin/amd64": "mac-x64", "windows/amd64": "win64", "windows/arm64": "win64"}[goos+"/"+goarch]
}

// userChromeGlob — где лежит распакованный Chrome for Testing (шаблон для Glob).
func userChromeGlob(goos, dataDir string) string {
	d := UserChromeDir(dataDir)
	switch goos {
	case "darwin":
		return filepath.Join(d, "*", "chrome-mac-*", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing")
	case "windows":
		return filepath.Join(d, "*", "chrome-win*", "chrome.exe")
	}
	return filepath.Join(d, "*", "chrome-linux64", "chrome")
}

// UserInstallAvailable — Chrome for Testing есть под эту платформу.
func UserInstallAvailable(s Sys) bool { return cftPlatform(s.GOOS, s.GOARCH) != "" }

// InstallUserChrome скачивает Chrome for Testing (канал Stable) в
// UserChromeDir(dataDir)/<версия>/ и возвращает путь к бинарнику. Ход — в out.
// Загрузка только по HTTPS с googlechromelabs.github.io и storage.googleapis.com;
// архив распаковывается без выхода за каталог версии.
func InstallUserChrome(ctx context.Context, s Sys, dataDir string, out io.Writer) (string, error) {
	plat := cftPlatform(s.GOOS, s.GOARCH)
	if plat == "" {
		return "", fmt.Errorf("Chrome for Testing has no build for %s/%s: install Chromium with the package manager", s.GOOS, s.GOARCH)
	}
	cl := &http.Client{Timeout: 10 * time.Minute}
	version, url, err := cftStable(ctx, cl, plat)
	if err != nil {
		return "", err
	}
	allowed := false
	for _, pre := range cftDownloadPrefixes {
		allowed = allowed || strings.HasPrefix(url, pre)
	}
	if !allowed {
		return "", fmt.Errorf("unexpected download address %s", url)
	}
	dir := filepath.Join(UserChromeDir(dataDir), version)
	if m, _ := filepath.Glob(filepath.Join(dir, "chrome-*")); len(m) > 0 {
		fmt.Fprintln(out, "Chrome for Testing "+version+" is already in "+dir)
		return findIn(s, dataDir)
	}
	fmt.Fprintln(out, "Downloading Chrome for Testing "+version+" ("+plat+") from "+url+" …")
	fmt.Fprintln(out, "Google Chrome comes from Google under the Google Chrome Terms of Service: https://www.google.com/chrome/terms/")
	if err := os.MkdirAll(UserChromeDir(dataDir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(UserChromeDir(dataDir), "download-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return "", err
	}
	tmp.Close()
	fmt.Fprintf(out, "Downloaded %d MB, unpacking into %s …\n", n>>20, dir)
	if err := unzipInto(tmp.Name(), dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return findIn(s, dataDir)
}

func findIn(s Sys, dataDir string) (string, error) {
	m, _ := filepath.Glob(userChromeGlob(s.GOOS, dataDir))
	if len(m) == 0 {
		return "", errors.New("Chrome for Testing unpacked, but its binary was not found")
	}
	return m[len(m)-1], nil
}

// cftStable — версия канала Stable и адрес архива chrome под платформу.
func cftStable(ctx context.Context, cl *http.Client, plat string) (version, url string, err error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", CFTVersionsURL, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("%s: %s", CFTVersionsURL, resp.Status)
	}
	var v struct {
		Channels map[string]struct {
			Version   string `json:"version"`
			Downloads struct {
				Chrome []struct {
					Platform string `json:"platform"`
					URL      string `json:"url"`
				} `json:"chrome"`
			} `json:"downloads"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&v); err != nil {
		return "", "", fmt.Errorf("Chrome for Testing versions: %w", err)
	}
	st, ok := v.Channels["Stable"]
	if !ok || st.Version == "" {
		return "", "", errors.New("Chrome for Testing: no Stable channel in the versions list")
	}
	for _, d := range st.Downloads.Chrome {
		if d.Platform == plat {
			return st.Version, d.URL, nil
		}
	}
	return "", "", fmt.Errorf("Chrome for Testing %s: no build for %s", st.Version, plat)
}

// unzipInto распаковывает архив в dir: пути только внутри dir, права файлов —
// из архива (бинарники Chrome исполняемые), символические ссылки не создаются.
func unzipInto(zipPath, dir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	root := filepath.Clean(dir)
	for _, f := range r.File {
		p := filepath.Join(root, filepath.FromSlash(f.Name))
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			return fmt.Errorf("archive entry escapes the folder: %s", f.Name)
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		perm := mode.Perm() | 0o600
		if mode.Perm()&0o111 != 0 {
			perm |= 0o100
		}
		if err := copyEntry(f, p, perm); err != nil {
			return err
		}
	}
	return nil
}

func copyEntry(f *zip.File, p string, perm os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, rc); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
