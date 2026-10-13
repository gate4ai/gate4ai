// SPDX-License-Identifier: FSL-1.1-ALv2

package e2e

import (
	"os"
	"strings"
	"testing"

	"path/filepath"

	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// Пример конфигурации из docs/quickstart.md проходит проверку схемы и CEL
// против манифестов официальных коннекторов (исходники монорепозитория).
func TestQuickstartConfigIsValid(t *testing.T) {
	b, err := os.ReadFile("../docs/quickstart.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "```yaml\n")
	j := strings.Index(s[i+8:], "```")
	yaml := s[i+8 : i+8+j]
	cat := policy.Catalog{}
	files, _ := filepath.Glob("../connectors/*/manifest.yaml")
	if len(files) == 0 {
		t.Skip("connector sources not found")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		m, err := manifest.Parse(b)
		if err != nil {
			t.Fatal(f, err)
		}
		for _, a := range m.Actions {
			cat[m.Full(a)] = a.ArgNames()
		}
	}
	if _, err := config.Parse([]byte(yaml), policy.Validator(cat)); err != nil {
		t.Fatal(err)
	}
}
