// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const fixtureUsage = `usage:
  gate4ai fixture scrub <file.json> [-o out.json] [--replace "real=fake"]... [--auto]
      replace personal data in a recorded browser session (browser.close) before it becomes a test fixture:
      --replace — exact values (names, policy numbers, addresses); --auto — e-mails, phone numbers, long numbers
      (ids, policies); the same value gets the same fake everywhere; without -o the file is rewritten`

// multi — повторяемый флаг.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phoneRe = regexp.MustCompile(`(\+7|\b8)[\s\-(]*\d{3}[\s\-)]*\d{3}[\s\-]*\d{2}[\s\-]*\d{2}\b`)
	longRe  = regexp.MustCompile(`\b\d{8,}\b`)
)

// fixture — офлайн-команды для фикстур тестов коннекторов.
func (e *Env) fixture(args []string) int {
	if len(args) == 0 || args[0] != "scrub" {
		fmt.Fprintln(e.Stderr, fixtureUsage)
		return ExitUsage
	}
	pos, fl := splitFlags(args[1:], map[string]bool{"o": true, "replace": true})
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	out := fs.String("o", "", "output file (default: rewrite the input)")
	var repl multi
	fs.Var(&repl, "replace", `"real=fake": replace this exact value (repeatable)`)
	auto := fs.Bool("auto", false, "also replace e-mails, phone numbers and long numbers")
	if fs.Parse(fl) != nil || len(pos) != 1 {
		fmt.Fprintln(e.Stderr, fixtureUsage)
		return ExitUsage
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return e.fail(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return e.fail(fmt.Errorf("%s: not JSON: %w", pos[0], err))
	}
	pairs := map[string]string{}
	for _, r := range repl {
		real, fake, ok := strings.Cut(r, "=")
		if !ok || real == "" {
			return e.fail(fmt.Errorf("--replace %q: want real=fake", r))
		}
		pairs[real] = fake
	}
	s := &scrubber{pairs: pairs, auto: *auto, fakes: map[string]string{}}
	doc = s.walk(doc)
	b, _ := json.MarshalIndent(doc, "", "  ")
	// Проверка: ни одно исходное значение не осталось.
	var left []string
	for real := range s.seen() {
		if strings.Contains(string(b), real) {
			left = append(left, real)
		}
	}
	if len(left) > 0 {
		sort.Strings(left)
		return e.fail(errors.New("values are still in the file after replacing (they are part of other text): " + strings.Join(left, ", ")))
	}
	dst := *out
	if dst == "" {
		dst = pos[0]
	}
	if err := os.WriteFile(dst, append(b, '\n'), 0o600); err != nil {
		return e.fail(err)
	}
	e.printJSON(map[string]any{"status": "ok", "file": dst, "replaced": s.count,
		"next": "check the file by eye: names of doctors, clinics, diagnoses and dates are not found automatically"})
	return ExitOK
}

type scrubber struct {
	pairs map[string]string
	auto  bool
	fakes map[string]string // найденное автоматически → замена
	count int
}

func (s *scrubber) seen() map[string]bool {
	out := map[string]bool{}
	for k := range s.pairs {
		out[k] = true
	}
	for k := range s.fakes {
		out[k] = true
	}
	return out
}

func (s *scrubber) walk(v any) any {
	switch t := v.(type) {
	case string:
		return s.text(t)
	case map[string]any:
		// По порядку ключей: одинаковые замены при каждом запуске.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := map[string]any{}
		for _, k := range keys {
			out[s.text(k)] = s.walk(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = s.walk(t[i])
		}
	}
	return v
}

func (s *scrubber) text(t string) string {
	// Сначала длинные значения: «Иванов Иван» раньше «Иван».
	keys := make([]string, 0, len(s.pairs))
	for k := range s.pairs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if n := strings.Count(t, k); n > 0 {
			t, s.count = strings.ReplaceAll(t, k, s.pairs[k]), s.count+n
		}
	}
	if !s.auto {
		return t
	}
	t = emailRe.ReplaceAllStringFunc(t, func(m string) string { return s.fake(m, "user%d@example.com") })
	t = phoneRe.ReplaceAllStringFunc(t, func(m string) string { return s.fake(m, "+7 900 000-00-%02d") })
	return longRe.ReplaceAllStringFunc(t, func(m string) string {
		if strings.Trim(m, "0") == "" || len(strings.TrimLeft(m, "0")) <= 2 {
			return m // уже замена
		}
		return s.fake(m, "%0"+fmt.Sprint(len(m))+"d")
	})
}

// fake — одна и та же замена для одного значения.
func (s *scrubber) fake(real, format string) string {
	if f, ok := s.fakes[real]; ok {
		return f
	}
	f := fmt.Sprintf(format, len(s.fakes)+1)
	s.fakes[real] = f
	s.count++
	return f
}
