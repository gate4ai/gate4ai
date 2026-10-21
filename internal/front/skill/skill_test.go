// SPDX-License-Identifier: AGPL-3.0-only

package skill

import (
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
	if strings.Contains(md, "--cc") {
		t.Error("optional args are not in examples")
	}
}
