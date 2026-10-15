// SPDX-License-Identifier: FSL-1.1-ALv2

package tray

import (
	"os"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/front/cli"
)

func TestLanguage(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if !Russian(env(map[string]string{"LANG": "ru_RU.UTF-8"})) || Russian(env(map[string]string{"LANG": "en_US.UTF-8"})) {
		t.Fatal("LANG")
	}
	if !Russian(env(map[string]string{"LANG": "en_US.UTF-8", "LC_MESSAGES": "ru_RU.UTF-8"})) || Russian(env(map[string]string{"LANG": "ru_RU", "GATE4AI_LANG": "en"})) {
		t.Fatal("priority")
	}
	for k, v := range messages {
		if v[0] == "" || v[1] == "" {
			t.Fatal("untranslated", k)
		}
	}
}

func TestProblem(t *testing.T) {
	_, body := Problem(&cli.UnavailableError{Addr: "/run/user/1/gate4ai/gate4ai.sock", Err: os.ErrNotExist})
	if !strings.Contains(body, "not running") || !strings.Contains(body, "gate4ai daemon start") || strings.Contains(body, "dial") {
		t.Fatal(body)
	}
	if !cli.NotRunning(&cli.UnavailableError{Addr: "/s", Err: os.ErrNotExist}) || cli.NotRunning(&cli.UnavailableError{Remote: true, Err: os.ErrNotExist}) {
		t.Fatal("NotRunning")
	}
}
