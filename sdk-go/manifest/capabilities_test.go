// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const notesYAML = `
connector: notes
version: "2"
runtime: wasm
capabilities:
  services:
    - { target: browser, contract: 1, actions: [open, act], params: { domains: ["${setting:sites}"] } }
  files: ["${setting:roots}/**"]
  storage: { quota_mb: 1 }
limits: { memory_mb: 64 }
actions:
  - name: send
    class: external
    verify: sent_folder
    calls: [browser.open, browser.act]
    input_schema:
      type: object
      properties:
        to: { type: array, items: { type: string, format: email } }
        limit: { type: integer, default: 20 }
      required: [to]
`

func TestParseAndResolve(t *testing.T) {
	m, err := Parse([]byte(notesYAML))
	if err != nil {
		t.Fatal(err)
	}
	if m.Runtime != RuntimeWasm || m.Limits.WithDefaults().TimeoutS != DefaultTimeoutS || m.Limits.WithDefaults().MemoryMB != 64 {
		t.Fatal(m.Runtime, m.Limits)
	}
	if d := m.Actions[0].InputSchema.Properties[1].Schema.Default; d != 20.0 {
		t.Fatalf("default not normalized to float64: %#v", d)
	}
	settings := map[string]any{
		"sites": []any{"lk.example", "b.example"},
		"roots": []any{"~/notes", "/srv/docs"},
	}
	r, err := m.Capabilities.Resolve(settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Services) != 1 || r.Services[0].Target != "browser" || r.Services[0].Contract != 1 || !r.Services[0].Allows("act") || r.Services[0].Allows("close") {
		t.Fatalf("services %#v", r.Services)
	}
	if fmt.Sprint(r.Services[0].Params["domains"]) != "[lk.example b.example]" {
		t.Fatalf("params %#v", r.Services[0].Params)
	}
	if !reflect.DeepEqual(r.Files, []string{"~/notes/**", "/srv/docs/**"}) || r.StorageBytes != 1<<20 {
		t.Fatal(r.Files, r.StorageBytes)
	}
	// Пустая настройка убирает запись: без sites и roots — нет сайтов и файлов.
	r, _ = m.Capabilities.Resolve(nil)
	if len(r.Services) != 1 || fmt.Sprint(r.Services[0].Params["domains"]) != "[]" || len(r.Files) != 0 || r.StorageBytes != 1<<20 {
		t.Fatalf("%#v", r)
	}
	// reveal: значение настройки — только алиас Vault.
	rm, err := Parse([]byte("connector: x\nruntime: native\nplatforms: [linux/amd64]\ncapabilities: { reveal: [\"${setting:key}\"] }"))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := rm.Capabilities.Resolve(map[string]any{"key": "@api"}); err != nil || !reflect.DeepEqual(r.Reveal, []string{"@api"}) {
		t.Fatal(r, err)
	}
	if _, err := rm.Capabilities.Resolve(map[string]any{"key": "hunter2"}); err == nil {
		t.Fatal("plain secret value accepted")
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]string{
		"relative files":   "capabilities: { files: [ \"notes/**\" ] }",
		"two templates":    "capabilities: { files: [ \"${setting:a}/${setting:b}\" ] }",
		"zero quota":       "capabilities: { storage: { quota_mb: 0 } }",
		"service no acts":  "capabilities: { services: [ { target: browser, contract: 1, actions: [] } ] }",
		"service contract": "capabilities: { services: [ { target: browser, contract: 0, actions: [open] } ] }",
		"reveal not alias": "runtime: native\nplatforms: [linux/amd64]\ncapabilities: { reveal: [ \"x\" ] }",
		"network removed":  "capabilities: { network: [ { host: a.com, scheme: https } ] }",
		"secrets removed":  "capabilities: { secrets: [ { alias: \"@x\", bind: [a.com] } ] }",
		"money no verify":  "actions: [ { name: pay, class: money } ]",
		"unknown field":    "capabilites: {}",
		"bad runtime":      "runtime: docker",
	}
	for name, body := range cases {
		if _, err := Parse([]byte("connector: bank\n" + body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte("connector: bank\nactions: [ { name: pay, class: money, verify: payment_status } ]")); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryAndSame(t *testing.T) {
	m, _ := Parse([]byte(notesYAML))
	s := strings.Join(m.Capabilities.Summary(), "\n")
	for _, want := range []string{"call browser (contract 1): open, act with {\"domains\":[\"${setting:sites}\"]}", "files ${setting:roots}/**", "storage 1 MB"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary lacks %q:\n%s", want, s)
		}
	}
	m2, _ := Parse([]byte(notesYAML))
	if !Same(m, m2) || !slices.Equal(m.Capabilities.Summary(), m2.Capabilities.Summary()) {
		t.Fatal("same manifests differ")
	}
	m2.Capabilities.Services[0].Actions = m2.Capabilities.Services[0].Actions[:1]
	if Same(m, m2) || slices.Equal(m.Capabilities.Summary(), m2.Capabilities.Summary()) {
		t.Fatal("changed capabilities not detected")
	}
}
