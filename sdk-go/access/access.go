// SPDX-License-Identifier: Apache-2.0

// Package access — списки доступа gate4.ai: домены Браузера и папки Файлов с
// режимом доступа. Одна реализация для хоста (решение политики, настройки
// встроенных компонентов), возможностей коннекторов (их сайты и папки) и
// Web UI. Решает самая конкретная подходящая строка; при равенстве — строже.
package access

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Режимы доступа. Для доменов: Allow, Read, Connector, Ask, Deny; для папок —
// ещё Append. Connector — только через WASM-коннектор (прямое действие агента
// запрещено, вызов коннектора разрешён); у коннектора такого режима нет.
const (
	Allow     = "allow"
	Read      = "read"
	Append    = "append"
	Connector = "connector"
	Ask       = "ask"
	Deny      = "deny"
)

// SiteModes и FolderModes — допустимые режимы; ConnectorSiteModes и
// ConnectorFolderModes — для строк коннектора (без Connector; у папок
// коннектора нет и Ask: коннектор не спрашивает владельца).
var (
	SiteModes            = []string{Allow, Read, Connector, Ask, Deny}
	FolderModes          = []string{Allow, Append, Read, Connector, Ask, Deny}
	ConnectorSiteModes   = []string{Allow, Read, Ask, Deny}
	ConnectorFolderModes = []string{Allow, Append, Read, Deny}
)

// strictness — порядок строгости режимов (больше — строже): при одинаковой
// конкретности шаблонов побеждает строже.
var strictness = map[string]int{Allow: 0, Append: 1, Read: 2, Connector: 3, Ask: 4, Deny: 5}

// Stricter — a строже b.
func Stricter(a, b string) bool { return strictness[a] > strictness[b] }

// Site — домен (с масками) и режим.
type Site struct {
	Domain string `json:"domain" yaml:"domain"`
	Access string `json:"access" yaml:"access"`
}

// Folder — папка (с масками) и режим.
type Folder struct {
	Path   string `json:"path" yaml:"path"`
	Access string `json:"access" yaml:"access"`
}

// Операции с файлами для FolderEffect.
const (
	OpRead   = "read"
	OpList   = "list"
	OpAppend = "append"
	OpWrite  = "write"
)

// CheckSite проверяет строку списка доменов (modes — допустимые режимы).
func CheckSite(s Site, modes []string) error {
	if !slices.Contains(modes, s.Access) {
		return fmt.Errorf("site %q: access %q (want %s)", s.Domain, s.Access, strings.Join(modes, " | "))
	}
	if !ValidDomain(s.Domain) {
		return fmt.Errorf("site %q: want a host like app.example.org, *.example.org or *", s.Domain)
	}
	return nil
}

// CheckFolder проверяет строку списка папок.
func CheckFolder(f Folder, modes []string) error {
	if !slices.Contains(modes, f.Access) {
		return fmt.Errorf("folder %q: access %q (want %s)", f.Path, f.Access, strings.Join(modes, " | "))
	}
	p := strings.TrimSpace(f.Path)
	if p == "" || !(strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.HasPrefix(p, "${setting:")) {
		return fmt.Errorf("folder %q: want an absolute path or ~/…", f.Path)
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("folder %q: bad pattern", f.Path)
		}
	}
	return nil
}

// ValidDomain — шаблон домена: метки из букв, цифр, дефиса и *, через точки;
// "*" — любой сайт.
func ValidDomain(d string) bool {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if d == "" || len(d) > 253 {
		return false
	}
	for _, l := range strings.Split(d, ".") {
		if l == "" {
			return false
		}
		for _, r := range l {
			if !(r == '*' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}

// MatchSite — строка для хоста URL (или голого хоста): самая конкретная
// (больше литеральных символов), при равенстве — строже. ok=false — ни одна
// не подходит.
func MatchSite(sites []Site, rawURL string) (Site, bool) {
	h := domain.HostOf(rawURL)
	if h == "" {
		return Site{}, false
	}
	var best Site
	ok, bestScore := false, -1
	for _, s := range sites {
		if !domain.HostMatches(h, s.Domain) {
			continue
		}
		score := literal(s.Domain)
		if !ok || score > bestScore || score == bestScore && Stricter(s.Access, best.Access) {
			best, ok, bestScore = s, true, score
		}
	}
	return best, ok
}

// literal — число не-метасимволов в шаблоне (конкретность).
func literal(p string) int {
	n := 0
	for _, r := range p {
		if r != '*' && r != '?' {
			n++
		}
	}
	return n
}

// MatchFolder — строка для пути (абсолютного или ~/…; home — домашний каталог
// для ~): шаблон без метасимволов — папка и всё внутри; "*" и "?" — внутри
// сегмента, "**" — любое число сегментов. Самая конкретная строка, при
// равенстве — строже. Сравнение — по настоящему пути (симлинки существующей
// части раскрыты; на macOS и Windows регистр не различается).
func MatchFolder(folders []Folder, p, home string) (Folder, bool) {
	np := normPath(p, home)
	if np == "" {
		return Folder{}, false
	}
	var best Folder
	ok, bestScore := false, -1
	for _, f := range folders {
		if !folderMatches(f.Path, np, home) {
			continue
		}
		score := literal(normPattern(f.Path, home))
		if !ok || score > bestScore || score == bestScore && Stricter(f.Access, best.Access) {
			best, ok, bestScore = f, true, score
		}
	}
	return best, ok
}

// FolderMatches — путь p попадает под шаблон папки.
func FolderMatches(pattern, p, home string) bool {
	np := normPath(p, home)
	return np != "" && folderMatches(pattern, np, home)
}

func folderMatches(pattern, np, home string) bool {
	pat := normPattern(pattern, home)
	if pat == "" {
		return false
	}
	if foldCase {
		np, pat = strings.ToLower(np), strings.ToLower(pat)
	}
	ps, pats := strings.Split(np, "/"), strings.Split(pat, "/")
	if !strings.ContainsAny(pat, "*?[") {
		// Папка и всё внутри.
		return len(ps) >= len(pats) && slices.Equal(ps[:len(pats)], pats)
	}
	return globSegs(ps, pats)
}

var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// Expand — путь с раскрытым ~ (home) в виде ОС, очищенный.
func Expand(p, home string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		p = home + p[1:]
	}
	return filepath.Clean(filepath.FromSlash(p))
}

// normPath — настоящий путь в виде с прямыми слэшами, "" — относительный или пустой.
func normPath(p, home string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	e := Expand(p, home)
	if !filepath.IsAbs(e) {
		return ""
	}
	return filepath.ToSlash(RealPath(e))
}

// normPattern — шаблон с раскрытым ~ и симлинками постоянной части.
func normPattern(pattern, home string) string {
	e := filepath.ToSlash(Expand(pattern, home))
	segs := strings.Split(e, "/")
	i := 0
	for i < len(segs) && !strings.ContainsAny(segs[i], "*?[\\") {
		i++
	}
	if i == 0 {
		return e
	}
	prefix := strings.Join(segs[:i], "/")
	if prefix == "" || !filepath.IsAbs(filepath.FromSlash(prefix)) {
		return e
	}
	r := filepath.ToSlash(RealPath(filepath.FromSlash(path.Clean(prefix))))
	if i == len(segs) {
		return r
	}
	return strings.TrimSuffix(r, "/") + "/" + strings.Join(segs[i:], "/")
}

// RealPath — путь с раскрытыми симлинками существующего префикса; при ошибке — как есть.
func RealPath(p string) string {
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

// Effect — решение (allow | ask | deny) по режиму доступа домена для действия:
// write — действие меняет что-то на сайте (класс write и выше). Режим
// Connector для прямого действия агента — deny; для вызова коннектора
// (viaConnector) — как Allow.
func Effect(mode string, write, viaConnector bool) string {
	switch mode {
	case Allow:
		return Allow
	case Read:
		if write {
			return Ask
		}
		return Allow
	case Connector:
		if viaConnector {
			return Allow
		}
		return Deny
	case Ask:
		return Ask
	}
	return Deny
}

// FolderEffect — решение по режиму доступа папки для операции с файлом.
func FolderEffect(mode, op string, viaConnector bool) string {
	switch mode {
	case Allow:
		return Allow
	case Append:
		if op == OpWrite {
			return Ask
		}
		return Allow
	case Read:
		if op == OpRead || op == OpList {
			return Allow
		}
		return Ask
	case Connector:
		if viaConnector {
			return Allow
		}
		return Deny
	case Ask:
		return Ask
	}
	return Deny
}

// OpOf — операция с файлом по имени действия files.* (read, list, append, write).
func OpOf(action string) string {
	switch {
	case strings.HasSuffix(action, ".list"):
		return OpList
	case strings.HasSuffix(action, ".append"):
		return OpAppend
	case strings.HasSuffix(action, ".write"):
		return OpWrite
	}
	return OpRead
}

// Open — строки, по которым доступ не закрыт совсем (всё, кроме Deny): они
// задают, куда встроенный компонент вообще может обратиться.
func OpenSites(sites []Site) (domains, blocked []string, blocklist bool) {
	for _, s := range sites {
		if s.Access == Deny {
			blocked = append(blocked, s.Domain)
			continue
		}
		if s.Domain == "*" {
			blocklist = true
			continue
		}
		domains = append(domains, s.Domain)
	}
	if blocklist {
		return nil, blocked, true
	}
	return domains, nil, false
}

// OpenFolders — папки, доступ к которым не закрыт совсем (всё, кроме Deny).
func OpenFolders(folders []Folder) []string {
	var out []string
	for _, f := range folders {
		if f.Access != Deny {
			out = append(out, f.Path)
		}
	}
	return out
}

// ParseSites — список доменов из настроек (JSON/YAML: список объектов
// {domain, access}); nil — настройки нет.
func ParseSites(v any) ([]Site, error) {
	items, ok := v.([]any)
	if !ok {
		if v == nil {
			return nil, nil
		}
		return nil, errors.New("sites: want a list of {domain, access}")
	}
	var out []Site
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, errors.New("sites: want a list of {domain, access}")
		}
		d, _ := m["domain"].(string)
		a, _ := m["access"].(string)
		out = append(out, Site{Domain: strings.ToLower(strings.TrimSpace(d)), Access: a})
	}
	return out, nil
}

// ParseFolders — список папок из настроек ({path, access}).
func ParseFolders(v any) ([]Folder, error) {
	items, ok := v.([]any)
	if !ok {
		if v == nil {
			return nil, nil
		}
		return nil, errors.New("folders: want a list of {path, access}")
	}
	var out []Folder
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, errors.New("folders: want a list of {path, access}")
		}
		p, _ := m["path"].(string)
		a, _ := m["access"].(string)
		out = append(out, Folder{Path: strings.TrimSpace(p), Access: a})
	}
	return out, nil
}

// SitesAny и FoldersAny — списки в виде настроек (для хранения в map[string]any).
func SitesAny(sites []Site) []any {
	out := make([]any, 0, len(sites))
	for _, s := range sites {
		out = append(out, map[string]any{"domain": s.Domain, "access": s.Access})
	}
	return out
}

func FoldersAny(folders []Folder) []any {
	out := make([]any, 0, len(folders))
	for _, f := range folders {
		out = append(out, map[string]any{"path": f.Path, "access": f.Access})
	}
	return out
}

// SetSite добавляет или заменяет строку домена; access "" — убирает строку.
func SetSite(sites []Site, domain, access string) []Site {
	domain = strings.ToLower(strings.TrimSpace(domain))
	out := slices.DeleteFunc(slices.Clone(sites), func(s Site) bool { return s.Domain == domain })
	if access != "" {
		out = append(out, Site{Domain: domain, Access: access})
	}
	return out
}

// SetFolder добавляет или заменяет строку папки; access "" — убирает строку.
func SetFolder(folders []Folder, p, access string) []Folder {
	p = strings.TrimSpace(p)
	out := slices.DeleteFunc(slices.Clone(folders), func(f Folder) bool { return f.Path == p })
	if access != "" {
		out = append(out, Folder{Path: p, Access: access})
	}
	return out
}
