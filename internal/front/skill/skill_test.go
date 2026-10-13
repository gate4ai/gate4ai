// SPDX-License-Identifier: FSL-1.1-ALv2

package skill

import (
	"strings"
	"testing"

	"github.com/gate4ai/sdk-go/manifest"
)

func TestGenerate(t *testing.T) {
	md := Generate([]Tool{
		{Name: "email.send", Connector: "email", Action: "send", Class: "external", Description: "Send an email",
			Args: []manifest.Arg{{Name: "to", Type: "string[]", Required: true, Format: "email"}, {Name: "subject", Type: "string", Required: true}, {Name: "cc", Type: "string[]"}}},
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
