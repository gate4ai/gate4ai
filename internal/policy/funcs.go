// SPDX-License-Identifier: FSL-1.1-ALv2

package policy

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gate4ai/sdk-go/domain"
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

// Glob сопоставляет путь с шаблоном: "*" и "?" — внутри сегмента, "**" — любое
// число сегментов. "~" раскрывается в обоих аргументах, ".." в пути схлопывается
// до сравнения, поэтому "~/Documents/obsidian/../.ssh/id" под "~/Documents/obsidian/**" не попадает.
func Glob(p, pattern string) bool {
	np := normPath(p)
	pat := filepath.ToSlash(expandHome(pattern))
	if np == "" || pat == "" {
		return false
	}
	return globSegs(strings.Split(np, "/"), strings.Split(pat, "/"))
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

// HostOf — хост http(s)-URL (правило — в sdk-go/domain, общее с коннекторами).
func HostOf(raw string) string { return domain.HostOf(raw) }

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
