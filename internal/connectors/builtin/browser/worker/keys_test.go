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
		",": ",", ";": ";", "A": "a", "ж": "ж", "Ctrl+,": "Control+,", "+": "+", "Ctrl++": "Control++", " ": "Space", "Shift+1": "Shift+1",
	} {
		k, err := ParseKey(in)
		if err != nil || k.String() != want {
			t.Errorf("%s: %v %v", in, k, err)
		}
	}
	for _, in := range []string{"", "F13", "Hyper+A", "Ctrl+", "Enter+Ctrl", "ab", "\x01"} {
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
	k, _ = ParseKey(",")
	if key, code, vk, text, mods := k.cdpKey(); key != "," || code != "Comma" || vk != 188 || text != "," || mods != 0 {
		t.Error(key, code, vk, text, mods)
	}
	k, _ = ParseKey("ж")
	if key, code, vk, text, _ := k.cdpKey(); key != "ж" || code != "" || vk != 0 || text != "ж" {
		t.Error(key, code, vk, text)
	}
	for r, want := range map[rune]string{'a': "a", 'Z': "Shift+z", ':': "Shift+:", ',': ",", '\n': "Enter", ' ': "Space", 'я': "я"} {
		if got := charKey(r).String(); got != want {
			t.Errorf("%q: %s", r, got)
		}
	}
	if key, _, _, text, mods := charKey('Z').cdpKey(); key != "Z" || text != "Z" || mods != 8 {
		t.Error(key, text, mods)
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
	if err := CheckAct(ActRequest{Selector: "x", Method: "typo"}); err == nil || !strings.Contains(err.Error(), "click, fill, append, paste, type, press, wait") {
		t.Fatal(err)
	}
	for _, r := range []ActRequest{
		{Selector: "x", Method: ActType},
		{Selector: "x", Method: ActType, Value: strings.Repeat("a", MaxTypeValue+1)},
		{Selector: "x", Method: ActType, Value: "a", DelayMS: MaxTypeDelay + 1},
		{Selector: "x", Method: ActType, Value: "a", Submit: "F13"},
		{Selector: "x", Method: ActType, Value: "a", Submit: "Ctrl+V"},
	} {
		if err := CheckAct(r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	if err := CheckAct(ActRequest{Selector: "x", Method: ActType, Value: "a, b", DelayMS: 20, Submit: "Enter"}); err != nil {
		t.Fatal(err)
	}
}
