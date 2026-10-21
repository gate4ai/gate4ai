// SPDX-License-Identifier: AGPL-3.0-only

package autostart

import (
	"slices"
	"strings"
	"testing"
)

// argvW — разбор командной строки по правилам CommandLineToArgvW (для аргументов
// после имени программы): 2n обратных косых + кавычка → n косых и переключение
// кавычек, 2n+1 → n косых и буквальная кавычка.
func argvW(cmd string) []string {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for i := 0; i < len(cmd); {
		c := cmd[i]
		switch {
		case c == '\\':
			n := 0
			for i < len(cmd) && cmd[i] == '\\' {
				n++
				i++
			}
			if i < len(cmd) && cmd[i] == '"' {
				cur.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					cur.WriteByte('"')
					i++
				}
			} else {
				cur.WriteString(strings.Repeat(`\`, n))
			}
			have = true
			continue
		case c == '"':
			inQ = !inQ
			have = true
		case (c == ' ' || c == '\t') && !inQ:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
		i++
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func TestWinQuote(t *testing.T) {
	for in, want := range map[string]string{
		`C:\data\`:           `"C:\data\\"`,
		`C:\Program Files\x`: `"C:\Program Files\x"`,
		`a"b`:                `"a\"b"`,
		`a\"b`:               `"a\\\"b"`,
		``:                   `""`,
	} {
		if got := winQuote(in); got != want {
			t.Errorf("winQuote(%q) = %q, want %q", in, got, want)
		}
	}
	// Регресс: `--home C:\data\` и следующий аргумент не склеиваются.
	args := []string{"--home", `C:\data\`, "--listen", `x "y" \\`, `\\server\share\`}
	var parts []string
	for _, a := range args {
		parts = append(parts, winQuote(a))
	}
	if got := argvW(strings.Join(parts, " ")); !slices.Equal(got, args) {
		t.Fatalf("round trip: %q, want %q", got, args)
	}
}
