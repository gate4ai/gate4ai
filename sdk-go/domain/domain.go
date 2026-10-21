// SPDX-License-Identifier: Apache-2.0

// Package domain — сопоставление адресов с шаблонами доменов: одно правило для
// политик хоста, возможностей коннекторов и самих коннекторов.
package domain

import (
	"fmt"
	"net"
	"net/url"
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
// "*.example.com" — только поддомены (не сам example.com).
func HostMatches(host, pattern string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	p := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pattern)), ".")
	if host == "" || p == "" {
		return false
	}
	if strings.HasPrefix(p, "*.") {
		return strings.HasSuffix(host, p[1:]) && len(host) > len(p)-1
	}
	return host == p
}

// Matches — хост URL совпадает с одним из шаблонов.
func Matches(raw string, patterns []string) bool {
	h := HostOf(raw)
	if h == "" {
		return false
	}
	for _, p := range patterns {
		if HostMatches(h, p) {
			return true
		}
	}
	return false
}

// Sites — какие сайты разрешены: белый список (только Domains) или чёрный
// (все, кроме Domains).
type Sites struct {
	Blocklist bool
	Domains   []string
}

// Allows — URL разрешён. Не http(s)-адрес не разрешён ни в каком режиме;
// чёрный список не открывает адреса этой машины и локальной сети (localhost,
// IP loopback, частные и link-local) — их можно разрешить только белым списком.
func (s Sites) Allows(raw string) bool {
	h := HostOf(raw)
	if h == "" {
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
	if o == "" {
		return false
	}
	for _, x := range origins {
		if OriginOf(x) == o {
			return true
		}
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
