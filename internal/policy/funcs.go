// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// homeDir — домашний каталог для раскрытия "~" (подменяется в тестах).
var homeDir = func() string {
	h, _ := os.UserHomeDir()
	return h
}

// expandHome раскрывает ведущий "~" и нормализует путь в форму со слешами.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.ToSlash(homeDir()) + p[1:]
	}
	return p
}

// normPath нормализует путь: "~", "..", двойные слеши. Относительные пути
// не совпадают ни с одним абсолютным шаблоном.
func normPath(p string) string {
	p = filepath.ToSlash(expandHome(p))
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// foldCase — файловая система по умолчанию нечувствительна к регистру
// (macOS, Windows): "~/Documents/Obsidian/Private/x" — тот же файл, что
// "~/Documents/obsidian/private/x", и правило обязано это видеть.
var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// Glob сопоставляет путь с шаблоном: "*" и "?" — внутри сегмента, "**" — любое
// число сегментов. "~" раскрывается в обоих аргументах, ".." в пути схлопывается
// до сравнения, поэтому "~/Documents/obsidian/../.ssh/id" под "~/Documents/obsidian/**" не попадает.
// Решение принимается по настоящему пути — тому, что откроет хост: симлинки
// в пути и в постоянной части шаблона раскрываются, на macOS и Windows
// регистр не различается. Неверный шаблон — false (в правиле — ошибка
// вычисления, см. GlobErr).
func Glob(p, pattern string) bool {
	ok, _ := GlobErr(p, pattern)
	return ok
}

// GlobErr — Glob с ошибкой неверного шаблона ("[abc").
func GlobErr(p, pattern string) (bool, error) {
	if err := CheckGlob(pattern); err != nil {
		return false, err
	}
	np := normPath(p)
	pat := filepath.ToSlash(expandHome(pattern))
	if np == "" || pat == "" {
		return false, nil
	}
	np = resolvePath(np)
	pat = resolvePattern(pat)
	if foldCase {
		np, pat = strings.ToLower(np), strings.ToLower(pat)
	}
	return globSegs(strings.Split(np, "/"), strings.Split(pat, "/")), nil
}

// CheckGlob проверяет синтаксис шаблона glob.
func CheckGlob(pattern string) error {
	for _, seg := range strings.Split(filepath.ToSlash(pattern), "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("glob: bad pattern %q", pattern)
		}
	}
	return nil
}

// resolvePath — путь после раскрытия симлинков существующей части (как у
// capability.Path); относительный путь не трогается.
func resolvePath(p string) string {
	if !filepath.IsAbs(filepath.FromSlash(p)) {
		return p
	}
	return filepath.ToSlash(realPath(filepath.FromSlash(p)))
}

// resolvePattern раскрывает симлинки в постоянной части шаблона (до первого
// сегмента с метасимволами): "~/Docs/**" при ~ → /private/home/u.
func resolvePattern(pat string) string {
	segs := strings.Split(pat, "/")
	i := 0
	for i < len(segs) && !strings.ContainsAny(segs[i], "*?[\\") {
		i++
	}
	if i == 0 {
		return pat
	}
	prefix := strings.Join(segs[:i], "/")
	if prefix == "" || !filepath.IsAbs(filepath.FromSlash(prefix)) {
		return pat
	}
	r := resolvePath(path.Clean(prefix))
	if i == len(segs) {
		return r
	}
	return strings.TrimSuffix(r, "/") + "/" + strings.Join(segs[i:], "/")
}

// realPath — путь с раскрытыми симлинками существующего префикса; ошибка — путь как есть.
func realPath(p string) string {
	cur := p
	var tail []string
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
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

func globSegs(ps, pats []string) bool {
	for len(pats) > 0 {
		if pats[0] == "**" {
			rest := pats[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(ps); i++ {
				if globSegs(ps[i:], rest) {
					return true
				}
			}
			return false
		}
		if len(ps) == 0 {
			return false
		}
		if ok, err := path.Match(pats[0], ps[0]); err != nil || !ok {
			return false
		}
		ps, pats = ps[1:], pats[1:]
	}
	return len(ps) == 0
}

// DomainMatches — хост URL совпадает с одним из шаблонов: "example.com" — точно,
// "*.example.com" — только поддомены (не сам example.com).
func DomainMatches(raw string, patterns []string) bool { return domain.Matches(raw, patterns) }

// EmailDomain — домен адреса электронной почты в нижнем регистре.
func EmailDomain(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndexByte(addr, '<'); i >= 0 {
		addr = strings.TrimSuffix(addr[i+1:], ">")
	}
	i := strings.LastIndexByte(addr, '@')
	if i < 0 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

// ActionMatches сопоставляет имя действия с шаблоном правила: "email.send",
// "payments.*", "*".
func ActionMatches(pattern, action string) bool {
	if pattern == "*" || pattern == action {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.HasPrefix(action, strings.TrimSuffix(pattern, "*"))
	}
	return false
}
