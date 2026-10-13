// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
)

const connectorsUsage = `usage:
  gate4ai connectors list
  gate4ai connectors install <file.g4c | name[@version]> [--dev] [--yes]
  gate4ai connectors update <name> [--yes]
  gate4ai connectors remove <name>
  gate4ai connectors refresh                       fetch the registry index (revocations)
  gate4ai connectors pack <dir> [--wasm file] [--key file] [-o file.g4c]
  gate4ai connectors keygen [-o key]`

// splitFlags отделяет позиционные аргументы от флагов (флаги могут идти после).
func splitFlags(args []string, withValue map[string]bool) (pos, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if withValue[strings.TrimLeft(a, "-")] && !strings.Contains(a, "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return
}

func (e *Env) connectors(ctx context.Context, args []string) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "pack":
		return e.pack(ctx, rest)
	case "keygen":
		return e.keygen(rest)
	}
	// Остальные команды — к daemon (сокет владельца).
	c := e.Client()
	switch sub {
	case "list":
		var res any
		if err := c.Do(ctx, "GET", "/v1/connectors", nil, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	case "install", "update":
		pos, fl := splitFlags(rest, nil)
		fs := flag.NewFlagSet(sub, flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		dev := fs.Bool("dev", false, "install a package not signed by a trusted key (your own build)")
		yes := fs.Bool("yes", false, "consent to the listed capabilities without asking")
		if fs.Parse(fl) != nil || len(pos) != 1 {
			fmt.Fprintln(e.Stderr, connectorsUsage)
			return ExitUsage
		}
		path, body := "/v1/connectors/install", map[string]any{"source": pos[0], "dev": *dev}
		if sub == "update" {
			path, body = "/v1/connectors/update", map[string]any{"name": pos[0]}
		} else if fi, err := os.Stat(pos[0]); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(pos[0])
			body["source"] = abs
		}
		var res any
		err := c.Do(ctx, "POST", path, body, &res)
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == "consent_required" {
			var r struct {
				Plan store.Plan `json:"plan"`
			}
			_ = json.Unmarshal(ae.Body, &r)
			if !e.consent(r.Plan, *yes) {
				e.printJSON(map[string]any{"status": "cancelled", "plan": r.Plan})
				return ExitDenied
			}
			body["consent"] = r.Plan.Capabilities
			err = c.Do(ctx, "POST", path, body, &res)
		}
		if err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	case "remove":
		if len(rest) != 1 {
			fmt.Fprintln(e.Stderr, connectorsUsage)
			return ExitUsage
		}
		var res any
		if err := c.Do(ctx, "POST", "/v1/connectors/remove", map[string]any{"name": rest[0]}, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	case "refresh":
		var res any
		if err := c.Do(ctx, "POST", "/v1/connectors/refresh", nil, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, connectorsUsage)
	return ExitUsage
}

func (e *Env) keygen(rest []string) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	out := fs.String("o", "", "write the private key to this file (0600)")
	if fs.Parse(rest) != nil {
		return ExitUsage
	}
	pub, priv, err := store.GenerateKey()
	if err != nil {
		return e.fail(err)
	}
	res := map[string]any{"public_key": pub}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(priv+"\n"), 0o600); err != nil {
			return e.fail(err)
		}
		res["private_key_file"] = *out
	} else {
		res["private_key"] = priv
	}
	e.printJSON(res)
	return ExitOK
}

// consent показывает возможности и спрашивает согласие владельца.
func (e *Env) consent(p store.Plan, yes bool) bool {
	fmt.Fprintf(e.Stderr, "Connector %s %s (trust: %s", p.Connector, p.Version, p.Trust)
	if p.Publisher != "" {
		fmt.Fprintf(e.Stderr, ", publisher: %s", p.Publisher)
	}
	fmt.Fprintln(e.Stderr, ")")
	if p.Installed != "" {
		fmt.Fprintf(e.Stderr, "Installed version: %s — the capabilities changed and need your consent again.\n", p.Installed)
	}
	if p.Repo != "" {
		fmt.Fprintln(e.Stderr, "Source:", p.Repo)
	}
	fmt.Fprintln(e.Stderr, "Action classes:", strings.Join(p.Classes, ", "))
	fmt.Fprintln(e.Stderr, "It will be able to:")
	if len(p.Capabilities) == 0 {
		fmt.Fprintln(e.Stderr, "  (nothing outside the host)")
	}
	for _, c := range p.Capabilities {
		fmt.Fprintln(e.Stderr, "  -", c)
	}
	for _, w := range p.Warnings {
		fmt.Fprintln(e.Stderr, "WARNING:", w)
	}
	if yes {
		return true
	}
	fmt.Fprint(e.Stderr, "Install? [y/N] ")
	line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
}

// pack собирает пакет коннектора из каталога исходников (Go) или готового модуля.
func (e *Env) pack(ctx context.Context, args []string) int {
	pos, fl := splitFlags(args, map[string]bool{"wasm": true, "key": true, "o": true})
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	wasmFile := fs.String("wasm", "", "ready connector.wasm (default: build the Go sources in <dir>)")
	keyFile := fs.String("key", "", "publisher private key file (gate4ai connectors keygen); without it the package is unsigned")
	out := fs.String("o", "", "output file (default <connector>-<version>.g4c)")
	if fs.Parse(fl) != nil || len(pos) != 1 {
		fmt.Fprintln(e.Stderr, connectorsUsage)
		return ExitUsage
	}
	dir := pos[0]
	man, err := os.ReadFile(filepath.Join(dir, store.ManifestFile))
	if err != nil {
		return e.fail(err)
	}
	bin := *wasmFile
	if bin == "" {
		tmp, err := os.MkdirTemp("", "g4apack")
		if err != nil {
			return e.fail(err)
		}
		defer os.RemoveAll(tmp)
		bin = filepath.Join(tmp, store.ModuleFile)
		if err := wasm.BuildGo(ctx, dir, bin); err != nil {
			return e.fail(err)
		}
	}
	wb, err := os.ReadFile(bin)
	if err != nil {
		return e.fail(err)
	}
	var key []byte
	if *keyFile != "" {
		if key, err = os.ReadFile(*keyFile); err != nil {
			return e.fail(err)
		}
	}
	pkg, err := packWith(man, wb, string(key))
	if err != nil {
		return e.fail(err)
	}
	p, err := store.Open(pkg)
	if err != nil {
		return e.fail(err)
	}
	if *out == "" {
		*out = p.Manifest.Connector + "-" + p.Manifest.Version + store.PackageExt
	}
	if err := os.WriteFile(*out, pkg, 0o644); err != nil {
		return e.fail(err)
	}
	e.printJSON(map[string]any{"package": *out, "connector": p.Manifest.Connector, "version": p.Manifest.Version,
		"digest": p.Sig.Digest, "sha256": store.SHA256(pkg), "signed_by": p.Sig.PublicKey, "capabilities": p.Manifest.Capabilities.Summary()})
	return ExitOK
}

func packWith(man, wb []byte, key string) ([]byte, error) {
	if key == "" {
		return store.Pack(man, wb, nil)
	}
	k, err := store.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	return store.Pack(man, wb, k)
}

func (e *Env) registry(args []string) int {
	if len(args) == 0 {
		args = []string{"help"}
	}
	sub := args[0]
	pos, fl := splitFlags(args[1:], map[string]bool{"key": true})
	fs := flag.NewFlagSet("registry", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	key := fs.String("key", "", "check: registry public key (ed25519:…); sign: registry private key file")
	if fs.Parse(fl) != nil || len(pos) != 1 {
		fmt.Fprintln(e.Stderr, "usage: gate4ai registry check <index.json> [--key ed25519:…] | sign <index.json> --key file")
		return ExitUsage
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return e.fail(err)
	}
	switch sub {
	case "check":
		var ix store.Index
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ix); err != nil {
			return e.fail(err)
		}
		if err := ix.Check(); err != nil {
			return e.fail(err)
		}
		res := map[string]any{"status": "ok", "connectors": len(ix.Connectors), "publishers": len(ix.Publishers), "revoked": len(ix.Revoked)}
		if *key != "" {
			sig, err := os.ReadFile(pos[0] + ".sig")
			if err != nil {
				return e.fail(err)
			}
			if _, err := store.ParseIndex(raw, strings.TrimSpace(string(sig)), []string{*key}); err != nil {
				return e.fail(err)
			}
			res["signature"] = "valid"
		}
		e.printJSON(res)
		return ExitOK
	case "sign":
		kb, err := os.ReadFile(*key)
		if err != nil {
			return e.fail(err)
		}
		sig, err := store.SignIndex(raw, string(kb))
		if err != nil {
			return e.fail(err)
		}
		if err := os.WriteFile(pos[0]+".sig", []byte(sig+"\n"), 0o644); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok", "signature": pos[0] + ".sig"})
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, "usage: gate4ai registry check <index.json> [--key ed25519:…] | sign <index.json> --key file")
	return ExitUsage
}
