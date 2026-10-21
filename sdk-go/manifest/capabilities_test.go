// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"reflect"
	"strings"
	"testing"
)

const mailYAML = `
connector: email
version: "2"
runtime: wasm
capabilities:
  network:
    - { host: "${setting:imap.host}", port: "${setting:imap.port}", scheme: tls }
    - { host: "${setting:smtp.host}", scheme: tls }
  secrets:
    - { alias: "${setting:imap.password}", bind: ["${setting:imap.host}"] }
  files: ["${setting:roots}/**"]
  storage: { quota_mb: 1 }
limits: { memory_mb: 64 }
actions:
  - name: send
    class: external
    verify: sent_folder
    input_schema:
      type: object
      properties:
        to: { type: array, items: { type: string, format: email } }
        limit: { type: integer, default: 20 }
      required: [to]
`

func TestParseAndResolve(t *testing.T) {
	m, err := Parse([]byte(mailYAML))
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
		"imap":  map[string]any{"host": "IMAP.example.com", "port": 993.0, "password": "@mail.work"},
		"smtp":  map[string]any{"host": "smtp.example.com"},
		"roots": []any{"~/notes", "/srv/docs"},
	}
	r, err := m.Capabilities.Resolve(settings)
	if err != nil {
		t.Fatal(err)
	}
	wantNet := []NetworkRule{{"imap.example.com", 993, "tls"}, {"smtp.example.com", 0, "tls"}}
	if !reflect.DeepEqual(r.Network, wantNet) {
		t.Fatalf("network %#v", r.Network)
	}
	if len(r.Secrets) != 1 || r.Secrets[0].Alias != "@mail.work" || r.Secrets[0].Bind[0] != "IMAP.example.com" {
		t.Fatalf("secrets %#v", r.Secrets)
	}
	if !reflect.DeepEqual(r.Files, []string{"~/notes/**", "/srv/docs/**"}) || r.StorageBytes != 1<<20 {
		t.Fatal(r.Files, r.StorageBytes)
	}
	// Пустая настройка убирает запись; порт не задан — любой.
	r, _ = m.Capabilities.Resolve(map[string]any{"imap": map[string]any{"host": "a.example"}})
	if len(r.Network) != 1 || r.Network[0].Port != 0 || len(r.Secrets) != 0 || len(r.Files) != 0 {
		t.Fatalf("%#v", r)
	}
	// Значение секрета в настройках — только алиас.
	if _, err := m.Capabilities.Resolve(map[string]any{"imap": map[string]any{"host": "a", "password": "hunter2"}}); err == nil {
		t.Fatal("plain secret value accepted")
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]string{
		"bad scheme":       "capabilities: { network: [ { host: a.com, scheme: ftp } ] }",
		"secret no bind":   "capabilities: { secrets: [ { alias: \"@x\", bind: [] } ] }",
		"secret not alias": "capabilities: { secrets: [ { alias: \"x\", bind: [a.com] } ] }",
		"relative files":   "capabilities: { files: [ \"notes/**\" ] }",
		"two templates":    "capabilities: { files: [ \"${setting:a}/${setting:b}\" ] }",
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
	m, _ := Parse([]byte(mailYAML))
	s := strings.Join(m.Capabilities.Summary(), "\n")
	for _, want := range []string{"network tls://${setting:imap.host}:${setting:imap.port}", "secret ${setting:imap.password} for ${setting:imap.host}", "storage 1 MB"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary lacks %q:\n%s", want, s)
		}
	}
	m2, _ := Parse([]byte(mailYAML))
	if !Same(m, m2) || !m.Capabilities.Equal(m2.Capabilities) {
		t.Fatal("same manifests differ")
	}
	m2.Capabilities.Network = m2.Capabilities.Network[:1]
	if Same(m, m2) || m.Capabilities.Equal(m2.Capabilities) {
		t.Fatal("changed capabilities not detected")
	}
}
