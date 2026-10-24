// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const sendYAML = `
connector: email
actions:
  - name: send
    class: external
    input_schema:
      type: object
      properties:
        to: { type: array, items: { type: string, format: email } }
        cc: { type: array, items: { type: string, format: email } }
        subject: { type: string, maxLength: 20 }
        body: { type: string }
        limit: { type: integer, default: 10, minimum: 1 }
        mode: { type: string, enum: [plain, html] }
        attachments:
          type: array
          items:
            type: object
            properties:
              name: { type: string }
              size: { type: integer }
            required: [name]
      required: [to, subject]
    output_schema:
      type: object
      properties:
        message_id: { type: string }
        recipients: { type: array, items: { type: string } }
      required: [message_id]
`

func sendAction(t *testing.T) Action {
	t.Helper()
	m, err := Parse([]byte(sendYAML))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := m.Find("send")
	return a
}

func TestNormalize(t *testing.T) {
	send := sendAction(t)
	got, err := send.Normalize(map[string]any{"to": "Landlord <LandLord@Example.com>", "subject": " Hi ",
		"attachments": []any{map[string]any{"name": " a.pdf "}}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"to": []any{"landlord@example.com"}, "cc": []any{}, "subject": "Hi", "body": "", "limit": 10.0, "mode": "",
		"attachments": []any{map[string]any{"name": "a.pdf", "size": 0.0}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	bad := []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"subject": "x"}, `missing required argument "to"`},
		{map[string]any{"to": []any{}, "subject": "x"}, "must not be empty"},
		{map[string]any{"to": "a@b.c", "subject": "x", "bcc": "x"}, "unknown argument(s): bcc"},
		{map[string]any{"to": "not-an-email", "subject": "x"}, "invalid email"},
		{map[string]any{"to": "a@b.c", "subject": 5.0}, "must be a string"},
		{map[string]any{"to": "a@b.c", "subject": "x", "limit": 1.5}, "must be an integer"},
		{map[string]any{"to": "a@b.c", "subject": "x", "limit": 0.0}, "out of range"},
		{map[string]any{"to": "a@b.c", "subject": strings.Repeat("x", 21)}, "too long"},
		{map[string]any{"to": "a@b.c", "subject": "x", "mode": "rtf"}, "must be one of"},
		{map[string]any{"to": []any{1.0}, "subject": "x"}, `"to[0]" must be a string`},
		{map[string]any{"to": "a@b.c", "subject": "x", "attachments": []any{map[string]any{}}}, `missing required argument "attachments[0].name"`},
		{map[string]any{"to": "a@b.c", "subject": "x", "attachments": []any{map[string]any{"name": "a", "x": 1.0}}}, "unknown argument(s): attachments[0].x"},
		{map[string]any{"to": "a@b.c", "subject": "x", "attachments": "a.pdf"}, "must be an array"},
	}
	for _, b := range bad {
		if _, err := send.Normalize(b.in); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%v: want %q, got %v", b.in, b.want, err)
		}
	}
}

func TestSchemaOrderAndJSON(t *testing.T) {
	send := sendAction(t)
	if got := strings.Join(send.ArgNames(), ","); got != "to,cc,subject,body,limit,mode,attachments" {
		t.Fatalf("order: %s", got)
	}
	// Порядок полей переживает JSON (манифест модуля приходит хосту в JSON).
	b, _ := json.Marshal(send)
	var back Action
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.ArgNames(), send.ArgNames()) {
		t.Fatalf("json order: %v", back.ArgNames())
	}
	s := send.JSONSchema()
	if s["required"].([]string)[0] != "to" || s["additionalProperties"] != false {
		t.Fatalf("%v", s)
	}
	att := s["properties"].(map[string]any)["attachments"].(map[string]any)["items"].(map[string]any)
	if att["additionalProperties"] != false || att["required"].([]string)[0] != "name" {
		t.Fatalf("nested: %v", att)
	}
}

func TestCheck(t *testing.T) {
	out := sendAction(t).OutputSchema
	if err := out.Check(map[string]any{"message_id": "<1@x>", "recipients": []any{"a@b.c"}}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{
		map[string]any{},
		map[string]any{"message_id": 1.0},
		map[string]any{"message_id": "x", "extra": true},
		map[string]any{"message_id": "x", "recipients": []any{1.0}},
		"x",
	} {
		if out.Check(v) == nil {
			t.Errorf("%v must fail", v)
		}
	}
}

func TestValidate(t *testing.T) {
	m := Manifest{Connector: "email", Actions: []Action{sendAction(t)}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.Actions = append(m.Actions, Action{Name: "send", Class: "read"})
	if m.Validate() == nil {
		t.Fatal("duplicate must fail")
	}
	m.Actions = []Action{{Name: "x", Class: "dangerous"}}
	if m.Validate() == nil {
		t.Fatal("bad class must fail")
	}
	bad := []string{
		"input_schema: { type: string }",
		"input_schema: { type: object, properties: { a: { type: array } } }",
		"input_schema: { type: object, properties: { a: { type: number, enum: [x] } } }",
		"input_schema: { type: object, properties: { a: { type: string, format: phone } } }",
		"input_schema: { type: object, properties: { a: { type: string } }, required: [b] }",
		"input_schema: { type: object, additionalProperties: true }",
		"input_schema: { type: object, properties: { a: { type: string, pattern: x } } }",
		"input_schema: { type: object, properties: { a: { type: string } } }\n    sensitive: [b]",
		"args: []",
	}
	for _, b := range bad {
		y := "connector: x\nactions:\n  - name: a\n    class: read\n    " + b + "\n"
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: must fail", b)
		}
	}
}

func TestBuiltinAndServices(t *testing.T) {
	const acts = "actions:\n  - { name: open, class: read, service_only: true }\n  - { name: read, class: read }\n"
	ok := []string{
		"connector: browser\nruntime: builtin\ncontract: 1\nservices: [open]\ncapabilities: { reveal: [\"${setting:model_key}\"] }\n" + acts,
		"connector: browser\nruntime: wasm\ncontract: 2\nservices: [open, read]\n" + acts,
	}
	for _, y := range ok {
		m, err := Parse([]byte(y))
		if err != nil {
			t.Fatalf("%v\n%s", err, y)
		}
		if !m.IsService("open") {
			t.Fatal("open is a service")
		}
	}
	bad := []string{
		"connector: b\nruntime: native\n" + acts,                                                   // среды native больше нет
		"connector: b\nruntime: wasm\nplatforms: [linux/amd64]\n" + acts,                           // platforms — неизвестное поле
		"connector: b\nservices: [open]\n" + acts,                                                  // нет contract
		"connector: b\ncontract: 1\nservices: [nope]\n" + acts,                                     // не действие
		"connector: b\ncontract: 1\nservices: [read]\n" + acts,                                     // service_only open не в services
		"connector: b\ncapabilities: { reveal: [\"@k\"] }\ncontract: 1\nservices: [open]\n" + acts, // reveal не для wasm
	}
	for _, y := range bad {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("accepted:\n%s", y)
		}
	}
}

func TestActionCalls(t *testing.T) {
	base := "connector: bank\ncapabilities:\n  services: [ { target: browser, contract: 1, actions: [open, act] } ]\nactions:\n"
	m, err := Parse([]byte(base + "  - { name: pay, class: money, verify: feed, calls: [browser.open, browser.act] }\n  - { name: feed, class: read, calls: [browser.open] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	pay, _ := m.Find("pay")
	if !pay.CallsAction("browser", "act") || pay.CallsAction("browser", "observe") {
		t.Fatal(pay.Calls)
	}
	sum := strings.Join(m.Summary(), "|")
	if !strings.Contains(sum, "action pay calls browser.open, browser.act") || !strings.Contains(sum, "action feed calls browser.open") {
		t.Fatal(sum)
	}
	for _, bad := range []string{"[browser.observe]", "[mail.send]", "[browser]", "[browser.open, browser.open]"} {
		if _, err := Parse([]byte(base + "  - { name: x, class: read, calls: " + bad + " }\n")); err == nil {
			t.Errorf("accepted calls %s", bad)
		}
	}
	if ClassRank("read") >= ClassRank("write") || ClassRank("external") >= ClassRank("money") || ClassRank("x") != -1 {
		t.Fatal("class order")
	}
}

func TestCredentialSlots(t *testing.T) {
	src := `
connector: lk_emias
capabilities:
  services:
    - { target: browser, contract: 1, actions: [open, login] }
credentials:
  - { slot: login, bind: ["https://login.mos.ru"], description: "mos.ru account" }
  - { slot: work, bind: ["https://${setting:host}"] }
actions:
  - name: summary
    class: write
    calls: [browser.open, browser.login]
`
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := m.Slot("login")
	if !ok || s.Bind[0] != "https://login.mos.ru" {
		t.Fatal(s, ok)
	}
	if o := m.Credentials[1].Origins(map[string]any{"host": "Lk.Example:443"}); len(o) != 1 || o[0] != "https://lk.example" {
		t.Fatal(o)
	}
	if !strings.Contains(strings.Join(m.Summary(), "\n"), "signs in on https://login.mos.ru with your account (slot login)") {
		t.Fatal(m.Summary())
	}
	for _, bad := range []string{
		`[{ slot: login, bind: [] }]`,
		`[{ slot: Login, bind: ["https://a.example"] }]`,
		`[{ slot: a, bind: ["https://a.example"] }, { slot: a, bind: ["https://b.example"] }]`,
		`[{ slot: a, bind: ["http://a.example"] }]`,
		`[{ slot: a, bind: ["https://a.example/login"] }]`,
		`[{ slot: a, bind: ["https://a.example"], form: true }]`,
	} {
		if _, err := Parse([]byte("connector: x\ncredentials: " + bad + "\nactions: []\n")); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
	if _, err := Parse([]byte("connector: x\ncredentials: [{ slot: a, bind: [\"http://127.0.0.1:8080\"] }]\nactions: []\n")); err != nil {
		t.Fatal("http on loopback is for tests:", err)
	}
}

func TestCallReasons(t *testing.T) {
	src := `connector: lk
version: "1"
runtime: wasm
capabilities:
  services: [ { target: browser, contract: 1, actions: [open, act], params: { domains: [lk.example] } } ]
actions:
  - name: visits
    class: write
    calls: [browser.open, browser.act]
    call_reasons: { browser.act: "раскрыть список приёмов" }
`
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := m.Find("visits")
	if got := a.CallsText(); got != "browser.open, browser.act (раскрыть список приёмов)" {
		t.Fatal(got)
	}
	if !slices.ContainsFunc(m.Summary(), func(s string) bool { return strings.Contains(s, "раскрыть список приёмов") }) {
		t.Fatal(m.Summary())
	}
	if _, err := Parse([]byte(strings.Replace(src, "browser.act:", "browser.observe:", 1))); err == nil || !strings.Contains(err.Error(), "not in calls") {
		t.Fatal("reason for an undeclared call:", err)
	}
}

func TestCredentialSlotTypes(t *testing.T) {
	src := `connector: lk
version: "1"
runtime: wasm
credentials:
  - { slot: login, bind: ["https://login.mos.ru"], types: [via], fields: [sms] }
actions: [ { name: a, class: read } ]
`
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.Slot("login")
	if s.Accepts(SlotForm) || !s.Accepts(SlotVia) {
		t.Fatal(s)
	}
	if !slices.ContainsFunc(m.Summary(), func(x string) bool { return strings.Contains(x, "a sign-in connector; enters fields sms") }) {
		t.Fatal(m.Summary())
	}
	if _, err := Parse([]byte(strings.Replace(src, "types: [via]", "types: [oauth]", 1))); err == nil {
		t.Fatal("unknown type")
	}
}

// Переводы title/description: язык — две строчные буквы; без перевода —
// английский title, без него — имя. Same учитывает переводы.
func TestI18n(t *testing.T) {
	y := `connector: files
title: Files
description: Files in allowed folders
i18n: { ru: { title: Файлы, description: Файлы в разрешённых папках } }
actions:
  - name: read
    title: Read a file
    description: Read a file
    class: read
    i18n: { ru: { title: Прочитать файл } }
  - { name: list, description: List, class: read }
`
	m, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if m.TitleIn("ru") != "Файлы" || m.TitleIn("de") != "Files" || m.DescriptionIn("ru") != "Файлы в разрешённых папках" || m.DescriptionIn("") != "Files in allowed folders" {
		t.Fatal(m.TitleIn("ru"), m.TitleIn("de"), m.DescriptionIn("ru"))
	}
	r, _ := m.Find("read")
	l, _ := m.Find("list")
	if r.TitleIn("ru") != "Прочитать файл" || r.TitleIn("en") != "Read a file" || r.DescriptionIn("ru") != "Read a file" || l.TitleIn("ru") != "list" {
		t.Fatal(r.TitleIn("ru"), r.TitleIn("en"), l.TitleIn("ru"))
	}
	if (Manifest{Connector: "x"}).TitleIn("ru") != "x" {
		t.Fatal("fallback to connector name")
	}
	m2, _ := Parse([]byte(strings.Replace(y, "Прочитать файл", "Читать", 1)))
	if Same(m, m2) || !Same(m, m) {
		t.Fatal("Same must see translations")
	}
	for _, bad := range []string{"RU", "rus", "r1"} {
		if _, err := Parse([]byte(strings.Replace(y, "{ ru: { title: Файлы", "{ "+bad+": { title: Файлы", 1))); err == nil || !strings.Contains(err.Error(), "i18n") {
			t.Fatal(bad, err)
		}
		if _, err := Parse([]byte(strings.Replace(y, "{ ru: { title: Прочитать", "{ "+bad+": { title: Прочитать", 1))); err == nil || !strings.Contains(err.Error(), "i18n") {
			t.Fatal(bad, err)
		}
	}
}
