// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/browserenv"
)

// serverEnvOf — сервер без экрана: что установлено, linger, хранилище ключей.
func serverEnvOf(have map[string]string, linger, keystore string) ServerEnv {
	return ServerEnv{
		Browser: browserenv.Sys{GOOS: "linux", GOARCH: "amd64", Getenv: func(string) string { return "" },
			LookPath: func(n string) (string, error) {
				if p, ok := have[n]; ok {
					return p, nil
				}
				return "", errors.New("no")
			},
			ReadFile: func(string) ([]byte, error) { return []byte("ID=debian\n"), nil }, Exists: func(string) bool { return false },
			Output: func(name string, _ ...string) (string, error) {
				if name == "fc-list" {
					return have["fc-list-out"], nil
				}
				return "Google Chrome 156", nil
			}},
		Linger:   func() string { return linger },
		Keystore: func(string) string { return keystore },
	}
}

// Сервер без экрана: каждая проблема названа с командой; после исправления — «всё в порядке».
func TestServerPage(t *testing.T) {
	u, d, fb, base := browserUI(t)
	_ = fb
	u.Autostart = &fakeAutostart{}
	u.Server = func() ServerEnv { return serverEnvOf(nil, "no", "file") }
	d.State.Seen("bot", "agent-socket", "")
	_, _, _, get := loginAs(t, u, base)
	p := get("/settings/server")
	for _, want := range []string{"требуют внимания", "loginctl enable-linger $USER", "gate4ai service install", "keys.json",
		"gate4ai relay register", "задайте пароль", "Подключите своего бота", "gate4ai browser setup", "browser_unavailable",
		"Экран для браузера", "bot работают через локальный сокет", `href="/settings/login"`} {
		if !strings.Contains(p, want) {
			t.Errorf("no %q", want)
		}
	}
	// Всё исправлено.
	u.Autostart = &runningAutostart{}
	u.Server = func() ServerEnv {
		return serverEnvOf(map[string]string{"google-chrome": "/usr/bin/google-chrome", "Xvfb": "/usr/bin/Xvfb", "fc-list-out": "a\nb\nc\nd\ne\nf\n"}, "yes", "keychain")
	}
	d.AddPublicURL(func() string { return "https://dev.example" })
	d.State.Set(protectionKey, `{"password":"x","telegram":true}`)
	u.Channels = nil
	p = get("/settings/server")
	for _, want := range []string{"работает службой", "linger включён", "системная связка", "https://dev.example", "пароль и подтверждение в Telegram",
		"виртуальный (Xvfb)", "/usr/bin/google-chrome"} {
		if !strings.Contains(p, want) {
			t.Errorf("no %q in\n%s", want, p)
		}
	}
	if strings.Contains(p, "gate4ai browser setup</code>") {
		t.Error("nothing to install")
	}
}

// Удалённо страница доступна только для чтения: без кнопок настроек.
func TestServerPageRemote(t *testing.T) {
	u, d, _, _ := browserUI(t)
	u.Autostart = &fakeAutostart{}
	u.Server = func() ServerEnv { return serverEnvOf(nil, "no", "file") }
	d.AddPublicURL(func() string { return "https://dev.example" })
	h := u.RemoteHandler()
	sid := random()
	u.mu.Lock()
	u.sessions[sid] = session{csrf: "c1", expires: u.Now().Add(time.Hour), remote: true}
	u.mu.Unlock()
	req := httptest.NewRequest("GET", "https://dev.example/ui/settings/server", nil)
	req.Host = "dev.example"
	req.AddCookie(&http.Cookie{Name: "g4a_rsession", Value: sid})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	p := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(p, "loginctl enable-linger") || strings.Contains(p, `href="/ui/settings/login"`) || strings.Contains(p, `class="side"`) {
		t.Fatal(rec.Code, p)
	}
	req = httptest.NewRequest("GET", "https://dev.example/ui/", nil)
	req.Host = "dev.example"
	req.AddCookie(&http.Cookie{Name: "g4a_rsession", Value: sid})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `href="/ui/settings/server"`) {
		t.Fatal("the remote note links to the server page")
	}
}

// runningAutostart — служба установлена и работает.
type runningAutostart struct{ fakeAutostart }

func (runningAutostart) State() map[string]any {
	return map[string]any{"installed": true, "enabled": true, "running": true}
}
