// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gate4ai/gate4ai/internal/knowledge"
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
	s := knowledge.NewScrubber(pairs, *auto)
	doc = s.Walk(doc)
	b, _ := json.MarshalIndent(doc, "", "  ")
	// Проверка: ни одно исходное значение не осталось.
	var left []string
	for real := range s.Seen() {
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
	e.printJSON(map[string]any{"status": "ok", "file": dst, "replaced": s.Count,
		"next": "check the file by eye: names of doctors, clinics, diagnoses and dates are not found automatically"})
	return ExitOK
}
