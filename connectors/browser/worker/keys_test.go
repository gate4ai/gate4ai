// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"errors"
	"strings"
	"testing"
)

func TestParseKey(t *testing.T) {
	for in, want := range map[string]string{
		"Enter": "Enter", "enter": "Enter", "ctrl+a": "Control+a", "Control+Shift+Enter": "Control+Shift+Enter",
		"Cmd+Enter": "Meta+Enter", "Shift+Tab": "Shift+Tab", "esc": "Escape", "Alt+ArrowLeft": "Alt+ArrowLeft", "7": "7", "Space": "Space",
	} {
		k, err := ParseKey(in)
		if err != nil || k.String() != want {
			t.Errorf("%s: %v %v", in, k, err)
		}
	}
	for _, in := range []string{"", "F13", "Hyper+A", "Ctrl+", "Enter+Ctrl"} {
		if _, err := ParseKey(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	for _, in := range []string{"Ctrl+V", "ctrl+c", "Meta+x"} {
		if _, err := ParseKey(in); !errors.Is(err, ErrClipboard) {
			t.Errorf("%s: %v", in, err)
		}
	}
	k, _ := ParseKey("Ctrl+A")
	if k.command() != "selectAll" {
		t.Error(k.command())
	}
	k, _ = ParseKey("Ctrl+Enter")
	if key, code, vk, text, mods := k.cdpKey(); key != "Enter" || code != "Enter" || vk != 13 || text != "" || mods != 2 {
		t.Error(key, code, vk, text, mods)
	}
	k, _ = ParseKey("Shift+a")
	if key, code, vk, text, mods := k.cdpKey(); key != "A" || code != "KeyA" || vk != 65 || text != "A" || mods != 8 {
		t.Error(key, code, vk, text, mods)
	}
}

func TestCheckActLimits(t *testing.T) {
	big := strings.Repeat("я", MaxActValue)
	if err := CheckAct(ActRequest{Selector: "textarea", Method: ActFill, Value: big}); err != nil {
		t.Fatal(err)
	}
	err := CheckAct(ActRequest{Selector: "textarea", Method: ActFill, Value: big + "я"})
	if err == nil || err.Error() != `argument "value" is too long: 262145 characters, the limit is 262144` {
		t.Fatal(err)
	}
	if err := CheckAct(ActRequest{Selector: "textarea", Method: ActAppend}); err == nil {
		t.Fatal("append without value")
	}
	if err := CheckAct(ActRequest{Selector: "x", Method: "type"}); err == nil || !strings.Contains(err.Error(), "click, fill, append, paste, press, wait") {
		t.Fatal(err)
	}
}
