// SPDX-License-Identifier: Apache-2.0

package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

const base = `connector: browser
runtime: wasm
contract: 1
services: [open, act]
actions:
  - name: open
    class: read
    service_only: true
    description: Open a page
    input_schema:
      type: object
      properties:
        url: { type: string, format: url }
        wait_ms: { type: integer, maximum: 60000 }
      required: [url]
    output_schema:
      type: object
      properties:
        url: { type: string }
        title: { type: string }
        tags: { type: array, items: { type: string } }
        page: { type: object, properties: { width: { type: number } } }
      required: [url, title]
  - name: act
    class: write
    input_schema:
      type: object
      properties:
        method: { type: string, enum: [click, fill] }
      required: [method]
  - name: read
    class: read
    input_schema: { type: object, properties: {} }
`

func parse(t *testing.T, y string) manifest.Manifest {
	t.Helper()
	m, err := manifest.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDiff(t *testing.T) {
	old := parse(t, base)
	cases := []struct {
		from, to string
		breaking bool
	}{
		{"      required: [url]\n    output_schema", "      required: [url, wait_ms]\n    output_schema", true}, // новое обязательное поле входа
		{"        wait_ms: { type: integer, maximum: 60000 }\n", "", true},                                      // поле входа удалено
		{"        wait_ms: { type: integer, maximum: 60000 }\n", "        wait_ms: { type: integer, maximum: 1000 }\n", true},
		{"        wait_ms: { type: integer, maximum: 60000 }\n", "        wait_ms: { type: integer }\n", false}, // диапазон шире
		{"        wait_ms: { type: integer, maximum: 60000 }\n", "        wait_ms: { type: string }\n", true},
		{"      required: [url]\n    output_schema", "      required: []\n    output_schema", false},
		{"        tags: { type: array, items: { type: string } }\n", "", true}, // поле выхода удалено
		{"      required: [url, title]", "      required: [url]", true},        // поле выхода стало необязательным
		{"        tags: { type: array, items: { type: string } }\n", "        tags: { type: array, items: { type: string } }\n        extra: { type: boolean }\n", false},
		{"enum: [click, fill]", "enum: [click, fill, press]", false}, // вход шире
		{"enum: [click, fill]", "enum: [click]", true},
		{"    class: write\n", "    class: money\n    verify: x\n", true},
		{"services: [open, act]", "services: [open, act, read]", false},
		{"services: [open, act]\n", "services: [open]\n", true},
	}
	for _, c := range cases {
		if !strings.Contains(base, c.from) {
			t.Fatalf("bad case %q", c.from)
		}
		nm := parse(t, strings.Replace(base, c.from, c.to, 1))
		err := Check(old, nm)
		if c.breaking != (err != nil) {
			t.Errorf("%q → %q: %v (changes %v)", c.from, c.to, err, Diff(old, nm))
		}
		if c.breaking {
			nm.Contract = 2
			if err := Check(old, nm); err != nil {
				t.Errorf("contract 2 must allow it: %v", err)
			}
		}
	}
	lower := parse(t, strings.Replace(base, "contract: 1", "contract: 1", 1))
	lower.Contract = 0
	if err := Check(old, lower); err == nil {
		t.Fatal("lower contract accepted")
	}
}

func TestGenCompiles(t *testing.T) {
	src, err := Gen(parse(t, base), "browserclient")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{
		"func (c Client) Open(in OpenInput) (OpenOutput, abi.ExecResult, error)",
		"URL    string `json:\"url\"`", "WaitMS int64  `json:\"wait_ms,omitempty\"`",
		"Tags  []string", "Page  OpenOutputPage", "const (\n\tConnector = \"browser\"\n\tContract  = 1\n)",
		"func (c Client) Act(in ActInput) (map[string]any, abi.ExecResult, error)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "func (c Client) Read") {
		t.Error("read is not a service")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	dir, err := os.MkdirTemp(".", "gentest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	os.WriteFile(filepath.Join(dir, "client.go"), src, 0o644)
	cmd := exec.Command("go", "vet", "./"+filepath.Base(dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated code does not compile: %v\n%s\n%s", err, out, s)
	}
	if _, err := Gen(parse(t, "connector: x\nactions: []\n"), "x"); err == nil {
		t.Fatal("no services accepted")
	}
}
