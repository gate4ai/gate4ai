// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gate4ai/sdk-go/contract"
	"github.com/gate4ai/sdk-go/manifest"
)

const contractUsage = `usage:
  gate4ai contract gen <manifest.yaml> [-o client.go] [--package name]   Go client of the connector's services (service_call)
  gate4ai contract diff <old manifest.yaml> <new manifest.yaml>          breaking changes need a higher contract`

// contract — контракты вызовов между коннекторами: клиент для потребителя и
// проверка совместимости для поставщика.
func (e *Env) contract(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(e.Stderr, contractUsage)
		return ExitUsage
	}
	pos, fl := splitFlags(args[1:], map[string]bool{"o": true, "package": true})
	fs := flag.NewFlagSet("contract", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	out := fs.String("o", "", "gen: output file (default: stdout)")
	pkg := fs.String("package", "", "gen: Go package name (default: <connector>client)")
	if fs.Parse(fl) != nil {
		fmt.Fprintln(e.Stderr, contractUsage)
		return ExitUsage
	}
	read := func(path string) (manifest.Manifest, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return manifest.Manifest{}, err
		}
		return manifest.Parse(b)
	}
	switch {
	case args[0] == "gen" && len(pos) == 1:
		m, err := read(pos[0])
		if err != nil {
			return e.fail(err)
		}
		name := *pkg
		if name == "" {
			name = strings.ReplaceAll(m.Connector, "_", "") + "client"
		}
		src, err := contract.Gen(m, name)
		if err != nil {
			return e.fail(err)
		}
		if *out == "" {
			_, _ = e.Stdout.Write(src)
			return ExitOK
		}
		if err := os.WriteFile(*out, src, 0o644); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok", "connector": m.Connector, "contract": m.Contract, "file": *out, "package": name})
		return ExitOK
	case args[0] == "diff" && len(pos) == 2:
		old, err := read(pos[0])
		if err != nil {
			return e.fail(err)
		}
		cur, err := read(pos[1])
		if err != nil {
			return e.fail(err)
		}
		changes := []string{}
		for _, c := range contract.Diff(old, cur) {
			changes = append(changes, c.String())
		}
		if err := contract.Check(old, cur); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok", "connector": cur.Connector, "contract": cur.Contract, "changes": changes})
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, contractUsage)
	return ExitUsage
}
