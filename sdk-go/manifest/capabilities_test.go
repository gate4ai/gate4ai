// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/access"
)

const notesYAML = `
connector: notes
version: "2"
runtime: wasm
capabilities:
  services:
    - { target: browser, contract: 1, actions: [open, act] }
  storage: { quota_mb: 1 }
sites:
  - { domain: "${setting:sites}", access: allow }
  - { domain: "*.example", access: ask }
folders:
  - { path: "${setting:roots}", access: append }
  - { path: "~/todo.md", access: read }
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
		"sites": []any{"lk.example", "B.example."},
		"roots": []any{"~/notes", "/srv/docs"},
	}
	r, err := m.Capabilities.Resolve(settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Services) != 1 || r.Services[0].Target != "browser" || r.Services[0].Contract != 1 || !r.Services[0].Allows("act") || r.Services[0].Allows("close") {
		t.Fatalf("services %#v", r.Services)
	}
	if r.StorageBytes != 1<<20 {
		t.Fatal(r.StorageBytes)
	}
	// Шаблон в домене раскрывается в несколько строк с тем же режимом;
	// домены — в нижнем регистре, без точки на конце.
	wantSites := []access.Site{{Domain: "lk.example", Access: access.Allow}, {Domain: "b.example", Access: access.Allow}, {Domain: "*.example", Access: access.Ask}}
	if got := m.ResolveSites(settings); !reflect.DeepEqual(got, wantSites) {
		t.Fatalf("sites %#v", got)
	}
	wantFolders := []access.Folder{{Path: "~/notes", Access: access.Append}, {Path: "/srv/docs", Access: access.Append}, {Path: "~/todo.md", Access: access.Read}}
	if got := m.ResolveFolders(settings); !reflect.DeepEqual(got, wantFolders) {
		t.Fatalf("folders %#v", got)
	}
	// Пустая настройка убирает запись: без sites и roots остаются только литералы.
	if got := m.ResolveSites(nil); !reflect.DeepEqual(got, wantSites[2:]) {
		t.Fatalf("sites without settings %#v", got)
	}
	if got := m.ResolveFolders(nil); !reflect.DeepEqual(got, wantFolders[2:]) {
		t.Fatalf("folders without settings %#v", got)
	}
	// reveal: значение настройки — только алиас Vault.
	rm, err := Parse([]byte("connector: x\nruntime: builtin\ncapabilities: { reveal: [\"${setting:key}\"] }"))
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
		"relative folder":    "folders: [ { path: \"notes\", access: allow } ]",
		"two templates":      "folders: [ { path: \"${setting:a}/${setting:b}\", access: allow } ]",
		"folder ask":         "folders: [ { path: \"~/notes\", access: ask } ]",
		"folder connector":   "folders: [ { path: \"~/notes\", access: connector } ]",
		"folder no access":   "folders: [ { path: \"~/notes\" } ]",
		"bad domain":         "sites: [ { domain: \"http://a.com/x\", access: allow } ]",
		"site connector":     "sites: [ { domain: a.com, access: connector } ]",
		"site append":        "sites: [ { domain: a.com, access: append } ]",
		"site two templates": "sites: [ { domain: \"${setting:a}.${setting:b}\", access: allow } ]",
		"files removed":      "capabilities: { files: [ \"~/notes/**\" ] }",
		"params removed":     "capabilities: { services: [ { target: browser, contract: 1, actions: [open], params: { domains: [a.com] } } ] }",
		"zero quota":         "capabilities: { storage: { quota_mb: 0 } }",
		"service no acts":    "capabilities: { services: [ { target: browser, contract: 1, actions: [] } ] }",
		"service contract":   "capabilities: { services: [ { target: browser, contract: 0, actions: [open] } ] }",
		"reveal not alias":   "runtime: builtin\ncapabilities: { reveal: [ \"x\" ] }",
		"network removed":    "capabilities: { network: [ { host: a.com, scheme: https } ] }",
		"secrets removed":    "capabilities: { secrets: [ { alias: \"@x\", bind: [a.com] } ] }",
		"money no verify":    "actions: [ { name: pay, class: money } ]",
		"unknown field":      "capabilites: {}",
		"bad runtime":        "runtime: docker",
	}
	for name, body := range cases {
		if _, err := Parse([]byte("connector: bank\n" + body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, body := range map[string]string{
		"money verified": "actions: [ { name: pay, class: money, verify: payment_status } ]",
		"site modes":     "sites: [ { domain: a.com, access: allow }, { domain: \"*\", access: ask }, { domain: c.com, access: deny } ]",
		"folder modes":   "folders: [ { path: /srv, access: allow }, { path: \"~/in\", access: append }, { path: \"~/docs/**/*.md\", access: read }, { path: \"~/.ssh\", access: deny } ]",
		"site template":  "sites: [ { domain: \"${setting:site}\", access: allow } ]",
	} {
		if _, err := Parse([]byte("connector: bank\n" + body)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSummaryAndSame(t *testing.T) {
	m, _ := Parse([]byte(notesYAML))
	s := strings.Join(m.Summary(), "\n")
	for _, want := range []string{
		"call browser (contract 1): open, act",
		"site ${setting:sites}: allow",
		"site *.example: ask",
		"folder ${setting:roots}: append",
		"folder ~/todo.md: read",
		"storage 1 MB",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(strings.Join(m.Capabilities.Summary(), "\n"), "folder") {
		t.Fatal("capabilities summary lists folders:", m.Capabilities.Summary())
	}
	m2, _ := Parse([]byte(notesYAML))
	if !Same(m, m2) || !slices.Equal(m.Summary(), m2.Summary()) {
		t.Fatal("same manifests differ")
	}
	m2.Capabilities.Services[0].Actions = m2.Capabilities.Services[0].Actions[:1]
	if Same(m, m2) || slices.Equal(m.Summary(), m2.Summary()) {
		t.Fatal("changed capabilities not detected")
	}
	m3, _ := Parse([]byte(notesYAML))
	m3.Folders[0].Access = access.Allow
	if Same(m, m3) || slices.Equal(m.Summary(), m3.Summary()) {
		t.Fatal("changed folder mode not detected")
	}
}
