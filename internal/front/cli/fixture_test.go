// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureScrub(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "session.json")
	os.WriteFile(in, []byte(`{"steps":[{"action":"observe","args":{},"result":{"url":"https://lk.example/?ehr=12345678901",
	  "text":"Иванов Иван Петрович, полис 7712345678901234, тел. +7 (916) 123-45-67, ivanov@mail.ru. Иванов — ещё раз: 7712345678901234"}}]}`), 0o600)
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	run := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return Run(context.Background(), &Env{Stdout: out, Stderr: errb, Getenv: func(string) string { return "" }}, args)
	}
	if code := run("fixture", "scrub", in, "--replace", "Иванов Иван Петрович=Петров Пётр Сергеевич", "--replace", "Иванов=Петров", "--auto"); code != ExitOK {
		t.Fatal(code, out, errb)
	}
	b, _ := os.ReadFile(in)
	got := string(b)
	for _, real := range []string{"Иванов", "7712345678901234", "916", "ivanov@mail.ru", "12345678901"} {
		if strings.Contains(got, real) {
			t.Errorf("%s is still there: %s", real, got)
		}
	}
	// Одно значение — одна замена: полис дважды заменён одинаково.
	if !strings.Contains(got, "Петров Пётр Сергеевич") || strings.Count(got, "0000000000000003") != 2 || !strings.Contains(got, "user1@example.com") {
		t.Fatal(got)
	}
	if code := run("fixture", "scrub", in, "--replace", "broken"); code == ExitOK {
		t.Fatal("bad --replace")
	}
}
