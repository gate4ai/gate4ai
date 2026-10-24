// SPDX-License-Identifier: Apache-2.0

// Package capability — проверка вызовов функций хоста по возможностям из
// манифеста коннектора (с подставленными настройками экземпляра). Одна
// реализация для хоста gate4.ai и для тестового хоста коннекторов.
package capability

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Checker — возможности одного экземпляра коннектора.
type Checker struct {
	R manifest.Resolved
	// Folders — папки коннектора с режимом (манифест или настройки владельца,
	// с подставленными настройками).
	Folders []access.Folder
	// Global — список папок Файлов gate4.ai (для коннекторов магазина): папка,
	// закрытая там (deny) или не перечисленная, недоступна и коннектору;
	// nil — не проверяется (компоненты ядра).
	Global []access.Folder
	// Home — домашний каталог для "~" в путях.
	Home string
}

// New — проверка по манифесту и настройкам экземпляра; folders — действующие
// папки коннектора (nil — из манифеста).
func New(m manifest.Manifest, settings map[string]any, home string) (*Checker, error) {
	r, err := m.Capabilities.Resolve(settings)
	if err != nil {
		return nil, err
	}
	return &Checker{R: r, Folders: m.ResolveFolders(settings), Home: home}, nil
}

func denied(f string, a ...any) error { return abi.Errorf(abi.CodeCapabilityDenied, f, a...) }

// Service — действие action поставщика target объявлено в capabilities.services.
func (c *Checker) Service(target, action string) (manifest.ServiceRule, error) {
	for _, s := range c.R.Services {
		if s.Target == target {
			if !s.Allows(action) {
				return s, denied("%s.%s is not declared in capabilities.services", target, action)
			}
			return s, nil
		}
	}
	return manifest.ServiceRule{}, denied("connector %s is not declared in capabilities.services", target)
}

// Storage — квота KV в байтах.
func (c *Checker) Storage() (int64, error) {
	if c.R.StorageBytes <= 0 {
		return 0, denied("storage is not declared in capabilities.storage")
	}
	return c.R.StorageBytes, nil
}

// Location — разрешённый путь: Real — настоящий путь; Root — каталог, внутри
// которого его можно открывать (os.Root), Rel — путь относительно Root;
// Exact — строка списка называет сам файл: последний компонент не должен
// оказаться симлинком.
type Location struct {
	Real, Root, Rel string
	Exact           bool
}

// Path — Locate для чтения; возвращает настоящий путь.
func (c *Checker) Path(p string) (string, error) {
	l, err := c.Locate(p, access.OpRead)
	return l.Real, err
}

// Locate проверяет путь по папкам коннектора (и списку Файлов gate4.ai, если
// он задан) для операции op (read | list | append | write) и возвращает
// настоящий путь с каталогом-корнем для открытия без выхода за пределы
// разрешённого (защита от подмены компонента пути симлинком после проверки).
// Решает самая конкретная строка; режим строки должен разрешать операцию
// (allow — всё, append — чтение и добавление, read — чтение), иначе
// capability_denied.
func (c *Checker) Locate(p string, op string) (Location, error) {
	if len(c.Folders) == 0 {
		return Location{}, denied("files are not declared in the connector's folders")
	}
	if !strings.HasPrefix(p, "~") && !filepath.IsAbs(filepath.FromSlash(p)) {
		return Location{}, denied("use an absolute path or ~/… (%s)", p)
	}
	f, ok := access.MatchFolder(c.Folders, p, c.Home)
	if !ok {
		return Location{}, denied("path %s is outside the connector's folders (%s)", p, folderList(c.Folders))
	}
	if eff := access.FolderEffect(f.Access, op, false); eff != access.Allow {
		return Location{}, denied("folder %s is %s for the connector: %s is not allowed", f.Path, f.Access, op)
	}
	if c.Global != nil {
		g, ok := access.MatchFolder(c.Global, p, c.Home)
		if !ok || g.Access == access.Deny {
			return Location{}, denied("path %s is closed in the Files access list of gate4.ai", p)
		}
	}
	real := access.RealPath(access.Expand(p, c.Home))
	root := access.RealPath(staticPrefix(access.Expand(f.Path, c.Home)))
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Location{}, denied("path %s is outside %s", p, f.Path)
	}
	if rel == "." {
		if fi, err := os.Stat(real); err == nil && !fi.IsDir() {
			// Строка называет сам файл.
			return Location{Real: real, Root: filepath.Dir(real), Rel: filepath.Base(real), Exact: true}, nil
		}
	}
	return Location{Real: real, Root: root, Rel: rel}, nil
}

// staticPrefix — часть шаблона до первого сегмента с метасимволами.
func staticPrefix(pattern string) string {
	segs := strings.Split(filepath.ToSlash(pattern), "/")
	i := 0
	for i < len(segs) && !strings.ContainsAny(segs[i], "*?[") {
		i++
	}
	if i == len(segs) {
		return filepath.FromSlash(pattern)
	}
	return filepath.FromSlash(strings.Join(segs[:i], "/"))
}

func folderList(fs []access.Folder) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path+": "+f.Access)
	}
	return strings.Join(out, ", ")
}
