// SPDX-License-Identifier: AGPL-3.0-only

package skill

import (
	"os"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

func TestGenerate(t *testing.T) {
	md := Generate([]Tool{
		{Name: "email.send", Connector: "email", Action: "send", Class: "external", Description: "Send an email",
			Schema: manifest.MustSchema(`{ type: object, properties: { to: { type: array, items: { type: string, format: email } }, subject: { type: string }, cc: { type: array, items: { type: string } } }, required: [to, subject] }`)},
		{Name: "files.read", Connector: "files", Action: "read", Class: "read", Description: "Read a file", Example: "gate4ai files read --path ~/notes/a.md"},
	})
	for _, want := range []string{"name: gate4ai", "denied_by_policy", "do not retry", "--wait 10m", "gate4ai status <id>", "verified: true",
		"gate4ai email send --to name@example.com --subject \"…\"", "gate4ai files read --path ~/notes/a.md", "### email", "untrusted_content"} {
		if !strings.Contains(md, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
	if strings.Contains(md, "`gate4ai email send --to name@example.com --subject \"…\" --cc") {
		t.Error("optional args are not in examples")
	}
	for _, want := range []string{"Arguments: `--to` (required), `--subject` (required), `--cc`", "agent_required", "`7`", "--value_file", "\"error\": {"} {
		if !strings.Contains(md, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
	if strings.Contains(md, "## Browser sessions") {
		t.Error("the browser guide is only for agents with the browser")
	}
}

// TestGenerateBrowser — SKILL.md перечисляет все действия браузера из каталога,
// их пределы и сеансы.
func TestGenerateBrowser(t *testing.T) {
	m, err := manifest.Parse(browserManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	var tools []Tool
	for _, a := range m.Actions {
		tools = append(tools, Tool{Name: m.Full(a), Connector: m.Connector, Action: a.Name, Description: a.Description, Class: a.Class, Schema: a.InputSchema, Example: a.Example})
	}
	md := Generate(tools)
	for _, a := range m.Actions {
		if !strings.Contains(md, "**`browser."+a.Name+"`**") {
			t.Errorf("SKILL.md lacks browser.%s", a.Name)
		}
	}
	for _, want := range []string{"## Browser sessions", "--session", "handoff", "one of: click, fill, append, paste, type, press, wait", "up to 262144 characters",
		"Ctrl+Enter", "--value_file", "of: fields, buttons, links", "close --all"} {
		if !strings.Contains(md, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
}

func browserManifest(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../connectors/browser/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
