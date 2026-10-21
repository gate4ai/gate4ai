// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// Всегда закрытые пути: ключи, учётки и данные браузеров не отдаются агенту,
// даже если корень — весь домашний каталог (~). Скрытые файлы и папки ниже
// корня (.ssh, .aws, .config…) открывает allow_hidden, остальное — только
// allow_paths.

// keyPatterns — имена файлов ключей и хранилищ паролей (без учёта регистра).
var keyPatterns = []string{"id_rsa*", "id_ed25519*", "id_ecdsa*", "id_dsa*",
	"*.pem", "*.key", "*.p12", "*.pfx", "*.kdbx", "*.keychain*"}

// secretDirs — каталоги секретов и данных относительно домашнего каталога.
var secretDirs = []string{
	"Library/Keychains",
	"Library/Cookies",
	"Library/Application Support/gate4ai",
	"Library/Application Support/Google/Chrome",
	"AppData/Local/gate4ai",
	"AppData/Local/Google/Chrome/User Data",
	"AppData/Roaming/Microsoft/Credentials",
	"AppData/Roaming/Microsoft/Protect",
}

// guard — проверка закрытых путей для одного вызова; все пути — настоящие
// (после ~, .. и симлинков).
type guard struct {
	roots       []string
	home        string
	allowHidden bool
	allow       []string
}

// newGuard; корни раскрывает хост (fs_stat — та же проверка возможностей и
// тот же «~», что у fs_*).
func newGuard(h *abi.Host, s Settings) (*guard, error) {
	g := &guard{allowHidden: s.AllowHidden}
	for _, r := range s.Roots {
		st, err := h.FSStat(r)
		if err != nil {
			return nil, err
		}
		g.roots = append(g.roots, st.RealPath)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		g.home = realPath(home)
	}
	for _, p := range s.AllowPaths {
		if p == "~" || strings.HasPrefix(p, "~/") {
			if home == "" {
				continue
			}
			p = home + p[1:]
		}
		if !filepath.IsAbs(filepath.FromSlash(p)) {
			continue
		}
		g.allow = append(g.allow, realPath(filepath.Clean(filepath.FromSlash(p))))
	}
	return g, nil
}

// realPath раскрывает симлинки существующей части пути (как capability).
func realPath(p string) string {
	cur, tail := p, []string{}
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r
		}
		if !errors.Is(err, os.ErrNotExist) {
			return p
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func within(p, root string) bool {
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// root — самый длинный корень, внутри которого путь.
func (g *guard) root(p string) (string, bool) {
	best, ok := "", false
	for _, r := range g.roots {
		if within(p, r) && len(r) >= len(best) {
			best, ok = r, true
		}
	}
	return best, ok
}

// keyName — имя похоже на ключ или хранилище паролей.
func keyName(name string) bool {
	name = strings.ToLower(name)
	for _, pat := range keyPatterns {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// why — причина, по которой путь закрыт ("" — открыт).
func (g *guard) why(p string) string {
	for _, a := range g.allow {
		if within(p, a) {
			return ""
		}
	}
	if !g.allowHidden {
		if r, ok := g.root(p); ok {
			rel, _ := filepath.Rel(r, p)
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				if part != "." && strings.HasPrefix(part, ".") {
					return "hidden files and folders are closed (allow_hidden)"
				}
			}
		}
	}
	if keyName(filepath.Base(p)) {
		return "key and password files are always closed (allow_paths)"
	}
	if g.home != "" {
		for _, d := range secretDirs {
			if within(p, filepath.Join(g.home, filepath.FromSlash(d))) {
				return "secret and browser data folders are always closed (allow_paths)"
			}
		}
	}
	return ""
}

// check — capability_denied, если путь закрыт.
func (g *guard) check(arg, real string) error {
	if w := g.why(real); w != "" {
		return abi.Errorf(abi.CodeCapabilityDenied, "path %s is protected: %s", arg, w)
	}
	return nil
}

// stat — настоящий путь аргумента и проверка закрытых путей до действия.
func (g *guard) stat(h *abi.Host, p string) (abi.FSStatResponse, error) {
	st, err := h.FSStat(p)
	if err != nil {
		return st, err
	}
	return st, g.check(p, st.RealPath)
}
