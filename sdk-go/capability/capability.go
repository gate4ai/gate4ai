// SPDX-License-Identifier: Apache-2.0

// Package capability — проверка вызовов функций хоста по возможностям из
// манифеста коннектора (с подставленными настройками экземпляра). Одна
// реализация для хоста gate4.ai и для тестового хоста коннекторов.
package capability

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Checker — возможности одного экземпляра коннектора.
type Checker struct {
	R manifest.Resolved
	// Home — домашний каталог для "~" в путях.
	Home string
}

// New — проверка по манифесту и настройкам экземпляра.
func New(m manifest.Manifest, settings map[string]any, home string) (*Checker, error) {
	r, err := m.Capabilities.Resolve(settings)
	if err != nil {
		return nil, err
	}
	return &Checker{R: r, Home: home}, nil
}

func denied(f string, a ...any) error { return abi.Errorf(abi.CodeCapabilityDenied, f, a...) }

// IsLoopback — адрес локальной машины.
func IsLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// Network — соединение с host:port по схеме разрешено.
func (c *Checker) Network(scheme, host string, port int) error {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if (scheme == manifest.SchemeHTTP || scheme == manifest.SchemeTCP) && !IsLoopback(host) {
		return denied("%s without TLS is allowed only for localhost (%s)", scheme, host)
	}
	for _, n := range c.R.Network {
		if n.Scheme == scheme && domain.HostMatches(host, n.Host) && (n.Port == 0 || n.Port == port) {
			return nil
		}
	}
	return denied("network %s://%s:%d is not declared in capabilities.network", scheme, host, port)
}

// Secret — алиас можно подставить для соединения scheme://host:port (scheme —
// https/http для http_request, tls/tcp для tls_dial). Привязка (bind) — точный
// хост ("api.example.com", без wildcard: поддомены — только явным списком) или
// точный origin ("https://api.example.com:8443", "imaps://imap.example.com:993").
// Для привязки-хоста схему и порт ограничивает Network (без TLS — только
// loopback). Алиас может быть объявлен несколько раз с разными привязками
// (пароль IMAP для SMTP): подходит любая.
func (c *Checker) Secret(alias, scheme, host string, port int) error {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	declared := false
	for _, s := range c.R.Secrets {
		if s.Alias != alias {
			continue
		}
		declared = true
		for _, b := range s.Bind {
			if bindMatches(b, scheme, host, port) {
				return nil
			}
		}
	}
	if declared {
		return denied("secret %s is not bound to %s", alias, host)
	}
	return denied("secret %s is not declared in capabilities.secrets", alias)
}

var defaultPorts = map[string]int{"https": 443, "http": 80, "imaps": 993, "smtps": 465}

func bindMatches(b, scheme, host string, port int) bool {
	b = strings.TrimSpace(b)
	if !strings.Contains(b, "://") {
		if strings.Contains(b, "*") {
			return false
		}
		return host != "" && strings.ToLower(strings.TrimSuffix(b, ".")) == host
	}
	u, err := url.Parse(b)
	if err != nil || u.User != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	bs := strings.ToLower(u.Scheme)
	bp := defaultPorts[bs]
	if p := u.Port(); p != "" {
		bp, _ = strconv.Atoi(p)
	}
	web := bs == "https" || bs == "http"
	switch {
	case scheme == "https" || scheme == "http":
		if bs != scheme {
			return false
		}
	case !web && scheme == manifest.SchemeTLS:
	default:
		return false
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".") == host && bp != 0 && bp == port
}

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

func (c *Checker) expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = c.Home + p[1:]
	}
	return filepath.Clean(filepath.FromSlash(p))
}

// realPath раскрывает симлинки существующей части пути.
func realPath(p string) (string, error) {
	cur := p
	var tail []string
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Path проверяет путь по capabilities.files и возвращает настоящий путь
// (после ~, .. и симлинков). Выход за пределы разрешённого — capability_denied.
func (c *Checker) Path(p string) (string, error) {
	l, err := c.Locate(p)
	return l.Real, err
}

// Location — разрешённый путь: Real — настоящий путь; Root — каталог, внутри
// которого его можно открывать (os.Root), Rel — путь относительно Root;
// Exact — разрешён только сам файл (правило без /**): последний компонент не
// должен оказаться симлинком.
type Location struct {
	Real, Root, Rel string
	Exact           bool
}

// Locate — Path с каталогом-корнем для открытия без выхода за пределы
// разрешённого (защита от подмены компонента пути симлинком после проверки).
func (c *Checker) Locate(p string) (Location, error) {
	if len(c.R.Files) == 0 {
		return Location{}, denied("files are not declared in capabilities.files")
	}
	if !strings.HasPrefix(p, "~") && !filepath.IsAbs(filepath.FromSlash(p)) {
		return Location{}, denied("use an absolute path or ~/… (%s)", p)
	}
	real, err := realPath(c.expand(p))
	if err != nil {
		return Location{}, abi.Errorf(abi.CodeIO, "%v", err)
	}
	for _, f := range c.R.Files {
		if root, ok := strings.CutSuffix(f, "/**"); ok {
			rr, err := realPath(c.expand(root))
			if err == nil && (real == rr || within(real, rr)) {
				rel, _ := filepath.Rel(rr, real)
				return Location{Real: real, Root: rr, Rel: rel}, nil
			}
			continue
		}
		if rr, err := realPath(c.expand(f)); err == nil && rr == real {
			return Location{Real: real, Root: filepath.Dir(real), Rel: filepath.Base(real), Exact: true}, nil
		}
	}
	return Location{}, denied("path %s is outside capabilities.files (%s)", p, strings.Join(c.R.Files, ", "))
}
