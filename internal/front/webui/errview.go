// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"regexp"
	"strings"
)

// ErrView — ошибка для окна Web UI: понятный текст и, если он заменил
// технический, исходный текст под «Подробности».
type ErrView struct {
	Text, Detail string
}

var (
	reLookup = regexp.MustCompile(`lookup ([^\s:]+)`)
	reURL    = regexp.MustCompile(`https?://([^\s/:"]+)`)
)

// errView переводит частые сетевые ошибки Go на человеческий язык (язык l):
// «dial tcp: lookup relay.gate4.ai …: i/o timeout» → «No connection to relay.gate4.ai…».
// Остальные ошибки показываются как есть (переводятся по каталогу, если там есть).
func errView(l, msg string) ErrView {
	host := ""
	if m := reLookup.FindStringSubmatch(msg); m != nil {
		host = m[1]
	} else if m := reURL.FindStringSubmatch(msg); m != nil {
		host = m[1]
	}
	where := or(host, tr(l, "the server"))
	var text Msg
	switch {
	case strings.Contains(msg, "lookup ") && (strings.Contains(msg, "no such host") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "server misbehaving")):
		text = M("No connection to %s: the address was not found (DNS did not answer). Check the internet connection and the address.", where)
	case strings.Contains(msg, "connection refused"):
		text = M("No connection to %s: the connection was refused. Check the address and that the server is running.", where)
	case strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "no route to host"):
		text = M("No connection to %s: the network is unreachable. Check the internet connection.", where)
	case strings.Contains(msg, "x509:") || strings.Contains(msg, "tls: "):
		text = M("Could not set up a secure connection to %s: a certificate problem.", where)
	case strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Client.Timeout"):
		text = M("No connection to %s: the server did not answer in time. Check the internet connection and try again.", where)
	default:
		return ErrView{Text: msg}
	}
	return ErrView{Text: text.In(l), Detail: msg}
}

// versionComment — комментарий версии политики словами на языке l
// («web: add agent bot» → «Web UI: agent bot added»).
func versionComment(l, s string) string {
	for _, v := range versionComments {
		if m := v.re.FindStringSubmatch(s); m != nil {
			args := make([]any, len(m)-1)
			for i, x := range m[1:] {
				args[i] = x
			}
			return M(v.f, args...).In(l)
		}
	}
	return tr(l, s)
}

var versionComments = []struct {
	re *regexp.Regexp
	f  string
}{
	{regexp.MustCompile(`^initial$`), "First version"},
	{regexp.MustCompile(`^connector (\S+): actions ask in profile (\S+)$`), "Connector %s added to %s: every action asks"},
	{regexp.MustCompile(`^(?:web: )?(?:setup: )?add agent (\S+)$`), "Agent %s added"},
	{regexp.MustCompile(`^web: remove agent (\S+)$`), "Agent %s removed"},
	{regexp.MustCompile(`^web: rule in (\S+)$`), "Rule changed for %s"},
	{regexp.MustCompile(`^web: remove rule (\S+)$`), "Rule %s removed"},
	{regexp.MustCompile(`^web: always allow (\S+) for (\S+)$`), "%s always allowed for %s"},
	{regexp.MustCompile(`^web: decision for (\S+) in (\S+)$`), "Decision for %s in %s"},
	{regexp.MustCompile(`^web: decision for (\S+) (\S+) in (\S+)$`), "Decision for %s (%s) in %s"},
	{regexp.MustCompile(`^web: connector (\S+) settings$`), "Settings of %s"},
	{regexp.MustCompile(`^web: template (\S+) for (\S+)$`), "Ready-made rules %s for %s"},
	{regexp.MustCompile(`^web: (.+)$`), "Web UI: %s"},
}
