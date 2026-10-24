// SPDX-License-Identifier: Apache-2.0

// Package domain — сопоставление адресов с шаблонами доменов: одно правило для
// политик хоста, возможностей коннекторов и самих коннекторов.
package domain

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// HostOf — хост http(s)-URL или голого имени хоста в нижнем регистре; "" — если
// это не http(s), есть userinfo или строка не разбирается.
func HostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if strings.ContainsAny(raw, "/?#@:") {
			return ""
		}
		return strings.TrimSuffix(strings.ToLower(raw), ".")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// HostMatches — хост совпадает с шаблоном: "example.com" — точно,
// "*.example.com" — поддомены и сам example.com, "*" — любой хост; "*" в
// метке — любые символы метки (shop-*.example.com).
func HostMatches(host, pattern string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	p := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pattern)), ".")
	if host == "" || p == "" {
		return false
	}
	if p == "*" {
		return true
	}
	if strings.HasPrefix(p, "*.") {
		base := p[2:]
		if host == base {
			return true
		}
		p = "*" + p[1:]
	}
	if !strings.Contains(p, "*") {
		return host == p
	}
	return globHost(host, p)
}

// globHost — шаблон с "*": "*" — любые символы, в том числе точки.
func globHost(host, p string) bool {
	parts := strings.Split(p, "*")
	if !strings.HasPrefix(host, parts[0]) {
		return false
	}
	host = host[len(parts[0]):]
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if i == len(parts)-1 {
			return strings.HasSuffix(host, part)
		}
		j := strings.Index(host, part)
		if j < 0 {
			return false
		}
		host = host[j+len(part):]
	}
	return true
}

// Matches — хост URL совпадает с одним из шаблонов.
func Matches(raw string, patterns []string) bool {
	h := HostOf(raw)
	return h != "" && slices.ContainsFunc(patterns, func(p string) bool { return HostMatches(h, p) })
}

// Sites — какие сайты разрешены: белый список (только Domains) или чёрный
// (все, кроме Domains).
type Sites struct {
	Blocklist bool
	Domains   []string
	// Blocked — сайты, закрытые в любом режиме (списки доступа gate4.ai).
	Blocked []string
}

// Allows — URL разрешён. Не http(s)-адрес не разрешён ни в каком режиме;
// чёрный список не открывает адреса этой машины и локальной сети (localhost,
// IP loopback, частные и link-local) — их можно разрешить только белым списком.
func (s Sites) Allows(raw string) bool {
	h := HostOf(raw)
	if h == "" || Matches(raw, s.Blocked) {
		return false
	}
	if !s.Blocklist {
		return Matches(raw, s.Domains)
	}
	return !isLocal(h) && !Matches(raw, s.Domains)
}

func isLocal(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}

// OriginOf — origin http(s)-URL: схема и хост в нижнем регистре, порт — только
// не по умолчанию ("https://login.example", "http://127.0.0.1:8080"); "" —
// если это не http(s) или есть userinfo.
func OriginOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && !(scheme == "https" && p == "443") && !(scheme == "http" && p == "80") {
		host += ":" + p
	}
	return scheme + "://" + host
}

// BindOrigin проверяет origin привязки учётки (bind): точный origin без пути,
// https или http только для адресов этой машины (разработка и тесты).
// Возвращает его в каноническом виде (OriginOf).
func BindOrigin(raw string) (string, error) {
	o := OriginOf(raw)
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case o == "" || err != nil:
		return "", fmt.Errorf("bind %q: want an origin like https://login.example", raw)
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return "", fmt.Errorf("bind %q: an origin has no path, query or fragment (scheme, host and port only)", raw)
	case u.Scheme == "http" && !isLoopback(strings.ToLower(u.Hostname())):
		return "", fmt.Errorf("bind %q: http is only for localhost; use https", raw)
	}
	return o, nil
}

// OriginIn — origin адреса raw — один из origins (канонических, BindOrigin).
func OriginIn(raw string, origins []string) bool {
	o := OriginOf(raw)
	return o != "" && slices.ContainsFunc(origins, func(x string) bool { return OriginOf(x) == o })
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
