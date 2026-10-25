// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

func TestConnectorsNew(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lk")
	run := func(args ...string) (int, string) {
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		code := Run(context.Background(), &Env{Stdout: out, Stderr: errb, Getenv: func(string) string { return "" }}, args)
		return code, out.String() + errb.String()
	}
	if code, out := run("connectors", "new", "my_lk", "--site", "https://account.example.org/", "-o", dir); code != ExitOK {
		t.Fatal(code, out)
	}
	m, err := manifest.Parse(must(os.ReadFile(filepath.Join(dir, "manifest.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := m.Find("summary")
	if m.Connector != "my_lk" || m.Runtime != manifest.RuntimeWasm || !ok || !a.CallsAction("browser", "observe") {
		t.Fatalf("%+v", m)
	}
	rule := m.Capabilities.Services[0]
	if rule.Target != "browser" || rule.Contract != 2 || len(m.Sites) != 1 || m.Sites[0].Domain != "account.example.org" || m.Sites[0].Access != access.Allow {
		t.Fatalf("%+v %+v", rule, m.Sites)
	}
	for _, f := range []string{"main.go", "connector.go", "connector_test.go"} {
		src := must(os.ReadFile(filepath.Join(dir, f)))
		if _, err := parser.ParseFile(token.NewFileSet(), f, src, parser.AllErrors); err != nil || !bytes.Contains(src, []byte("MyLk")) {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := hosttest.LoadFixture(filepath.Join(dir, "testdata", "session.json")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(must(os.ReadFile(filepath.Join(dir, "go.mod"))), []byte("github.com/gate4ai/gate4ai v")) {
		t.Fatal("go.mod")
	}
	if code, out := run("connectors", "new", "my_lk", "--site", "account.example.org", "-o", dir); code == ExitOK || !strings.Contains(out, "already exists") {
		t.Fatal("existing folder:", code, out)
	}
	for _, bad := range [][]string{{"Bad-Name", "--site", "x.org"}, {"ok", "--site", "a/b"}, {"ok"}} {
		if code, _ := run(append([]string{"connectors", "new"}, bad...)...); code == ExitOK {
			t.Errorf("%v accepted", bad)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestStaleDaemon(t *testing.T) {
	if w := staleDaemon(map[string]any{"version": version.Version}); w != "" {
		t.Fatal(w)
	}
	for _, h := range []map[string]any{{"version": "0.0.1"}} {
		if w := staleDaemon(h); !strings.Contains(w, "gate4ai daemon restart") {
			t.Fatal(h, w)
		}
	}
}
