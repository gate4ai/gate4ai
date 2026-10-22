// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/gate4ai/sdk-go/registry"

	"github.com/gate4ai/gate4ai/internal/connectors/native"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
)

const connectorsUsage = `usage:
  gate4ai connectors list
  gate4ai connectors new <name> --site <domain> [-o folder]   a WASM connector over the browser to start from
  gate4ai connectors install <file.g4c | https://…/file.g4c | name[@version]> [--dev] [--yes]
  gate4ai connectors install <source folder> [--yes] [--watch]   build from Go sources (your own build, like --dev)
  gate4ai connectors update <name> [--yes] [--watch]           a connector from a folder is rebuilt from it;
                                                                --watch: rebuild on every change of the sources
  gate4ai connectors remove <name>
  gate4ai connectors logs <name> [--limit 50] [--since 10m]   the connector's log: its log(), stderr, daemon messages
  gate4ai connectors refresh                       fetch the registry index (revocations)
  gate4ai connectors pack <dir> [--wasm file] [--key file] [-o file.g4c]
                                                   runtime native: a package per manifest.platforms and
                                                   <connector>-<version>.platforms.json (--platform os/arch,… — only these;
                                                   one platform with -o file.g4c — just that package)
  gate4ai connectors verify <dir> <file.g4c | list.platforms.json | URL>   rebuild from sources and compare (registry review)
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
	case "verify":
		return e.verifyPackage(ctx, rest)
	case "new":
		return e.scaffold(rest)
	}
	// Остальные команды — к daemon (сокет владельца).
	c := e.Client()
	switch sub {
	case "list":
		var res struct {
			Connectors []any `json:"connectors"`
		}
		var raw json.RawMessage
		if err := c.Do(ctx, "GET", "/v1/connectors", nil, &raw); err != nil {
			return e.fail(err)
		}
		if json.Unmarshal(raw, &res) == nil && len(res.Connectors) == 0 {
			fmt.Fprintln(e.Stderr, "gate4ai: no connectors installed: gate4ai connectors install browser (from the store) or gate4ai connectors install <file.g4c>")
		}
		var out any
		_ = json.Unmarshal(raw, &out)
		e.out(out, connectorsTable(out))
		return ExitOK
	case "install", "update":
		pos, fl := splitFlags(rest, nil)
		fs := flag.NewFlagSet(sub, flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		dev := fs.Bool("dev", false, "install a package not signed by a trusted key (your own build)")
		yes := fs.Bool("yes", false, "consent to the listed capabilities without asking")
		watch := fs.Bool("watch", false, "keep running and rebuild the connector from its source folder on every change")
		if fs.Parse(fl) != nil || len(pos) != 1 {
			fmt.Fprintln(e.Stderr, connectorsUsage)
			return ExitUsage
		}
		path, body := "/v1/connectors/install", map[string]any{"source": pos[0], "dev": *dev}
		folder := ""
		if sub == "update" {
			path, body = "/v1/connectors/update", map[string]any{"name": pos[0]}
			var err error
			if folder, err = e.installedFolder(ctx, pos[0]); err != nil {
				return e.fail(err)
			}
		} else if dir, ok := store.SourceFolder(pos[0]); ok {
			folder = dir
		} else if fi, err := os.Stat(pos[0]); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(pos[0])
			body["source"] = abs
		} else if err == nil {
			return e.fail(fmt.Errorf("%s: no %s in this folder", pos[0], store.ManifestFile))
		}
		if *watch && folder == "" {
			return e.fail(errors.New("--watch needs a connector from a source folder: gate4ai connectors install <folder> --watch"))
		}
		var res any
		code := ExitOK
		if folder != "" {
			res, code = e.installFolder(ctx, folder, *yes)
		} else {
			res, code = e.installCall(ctx, path, body, *yes)
		}
		if code == ExitOK {
			e.printJSON(res)
		}
		if code != ExitOK || !*watch {
			return code
		}
		return e.watchConnector(ctx, folder, *yes)
	case "remove":
		if len(rest) != 1 {
			fmt.Fprintln(e.Stderr, connectorsUsage)
			return ExitUsage
		}
		return e.call(ctx, c, "POST", "/v1/connectors/remove", map[string]any{"name": rest[0]})
	case "logs":
		pos, fl := splitFlags(rest, map[string]bool{"limit": true, "since": true})
		fs := flag.NewFlagSet(sub, flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		limit := fs.Int("limit", 50, "the last N lines (0 — all kept)")
		since := fs.String("since", "", "only lines newer than this, e.g. 10m")
		if fs.Parse(fl) != nil || len(pos) != 1 {
			fmt.Fprintln(e.Stderr, connectorsUsage)
			return ExitUsage
		}
		q := url.Values{"name": {pos[0]}, "limit": {fmt.Sprint(*limit)}}
		if *since != "" {
			q.Set("since", *since)
		}
		var res struct {
			Connector string `json:"connector"`
			Lines     []struct {
				Time  time.Time `json:"time"`
				Level string    `json:"level"`
				Msg   string    `json:"msg"`
			} `json:"lines"`
		}
		if err := c.Do(ctx, "GET", "/v1/connectors/logs?"+q.Encode(), nil, &res); err != nil {
			return e.fail(err)
		}
		e.out(res, func(w io.Writer) {
			for _, l := range res.Lines {
				fmt.Fprintf(w, "%s\t%s\t%s\n", l.Time.Local().Format("15:04:05"), l.Level, l.Msg)
			}
		})
		return ExitOK
	case "refresh":
		return e.call(ctx, c, "POST", "/v1/connectors/refresh", nil)
	}
	fmt.Fprintln(e.Stderr, connectorsUsage)
	return ExitUsage
}

// installCall — install / update у daemon; если нужно согласие на возможности,
// показывает их владельцу и повторяет запрос.
func (e *Env) installCall(ctx context.Context, path string, body map[string]any, yes bool) (any, int) {
	c := e.Client()
	var res any
	err := c.Do(ctx, "POST", path, body, &res)
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == "consent_required" {
		var r struct {
			Plan store.Plan `json:"plan"`
		}
		_ = json.Unmarshal(ae.Body, &r)
		if ok, answered := e.consent(r.Plan, yes); !ok {
			msg := "not installed: you did not consent (answer y, or pass --yes)"
			if !answered && !e.Interactive {
				msg = "not installed: consent is needed and this is not an interactive terminal; pass --yes to consent to the capabilities above"
			}
			fmt.Fprintln(e.Stderr, "gate4ai: "+msg)
			e.printJSON(map[string]any{"status": "cancelled", "message": msg, "plan": r.Plan})
			return nil, ExitDenied
		}
		body["consent"] = r.Plan.Capabilities
		err = c.Do(ctx, "POST", path, body, &res)
	}
	if err != nil {
		return nil, e.fail(err)
	}
	return res, ExitOK
}

// installedFolder — папка исходников установленного коннектора ("" — он не из папки).
func (e *Env) installedFolder(ctx context.Context, name string) (string, error) {
	var res struct {
		Connectors []struct {
			Connector string `json:"connector"`
			Source    string `json:"source"`
		} `json:"connectors"`
	}
	if err := e.Client().Do(ctx, "GET", "/v1/connectors", nil, &res); err != nil {
		return "", err
	}
	for _, c := range res.Connectors {
		if c.Connector == name && strings.HasPrefix(c.Source, store.SourceDir) {
			return strings.TrimPrefix(c.Source, store.SourceDir), nil
		}
	}
	return "", nil
}

// installFolder собирает коннектор из папки исходников в окружении владельца
// (его Go, go.work, GOFLAGS) и ставит пакет как свою сборку; источником
// установки daemon записывает папку.
func (e *Env) installFolder(ctx context.Context, folder string, yes bool) (any, int) {
	man, err := os.ReadFile(filepath.Join(folder, store.ManifestFile))
	if err != nil {
		return nil, e.fail(err)
	}
	wb, _, err := buildModule(ctx, folder)
	if err != nil {
		return nil, e.fail(err)
	}
	pkg, err := store.Pack(man, wb, nil)
	if err != nil {
		return nil, e.fail(err)
	}
	tmp, err := os.CreateTemp("", "g4adev-*"+store.PackageExt)
	if err != nil {
		return nil, e.fail(err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(pkg)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, e.fail(err)
	}
	return e.installCall(ctx, "/v1/connectors/install",
		map[string]any{"source": tmp.Name(), "dev": true, "origin": store.SourceDir + folder}, yes)
}

// WatchInterval — как часто --watch проверяет исходники.
var WatchInterval = time.Second

// watchConnector пересобирает коннектор из папки исходников при каждом их
// изменении (update у daemon), пока команду не прервут.
func (e *Env) watchConnector(ctx context.Context, folder string, yes bool) int {
	fmt.Fprintf(e.Stderr, "gate4ai: watching %s; the connector is rebuilt on every change (Ctrl+C to stop)\n", folder)
	last := sourcesStamp(folder)
	for {
		select {
		case <-ctx.Done():
			return ExitOK
		case <-time.After(WatchInterval):
		}
		cur := sourcesStamp(folder)
		if cur == last {
			continue
		}
		last = cur
		fmt.Fprintln(e.Stderr, "gate4ai: sources changed, rebuilding")
		// ошибка сборки печатается, наблюдение продолжается
		if r, code := e.installFolder(ctx, folder, yes); code == ExitOK {
			e.printJSON(r)
		}
	}
}

// sourcesStamp — отпечаток исходников коннектора: пути, размеры и время
// изменения .go, go.mod, go.sum и манифеста (скрытые папки и vendor не смотрим).
func sourcesStamp(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n := d.Name()
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(n, ".") || n == "vendor" || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(n, ".go") && n != "go.mod" && n != "go.sum" && n != store.ManifestFile {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

func (e *Env) keygen(rest []string) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	out := fs.String("o", "", "write the private key to this file (0600)")
	if fs.Parse(rest) != nil {
		return ExitUsage
	}
	pub, priv, err := registry.GenerateKey()
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
// answered — владелец что-то ответил (иначе — пустой ввод или нет терминала).
func (e *Env) consent(p store.Plan, yes bool) (ok, answered bool) {
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
		return true, true
	}
	fmt.Fprint(e.Stderr, "Install? [y/N] ")
	line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
	a := strings.TrimSpace(line)
	return strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"), a != ""
}

// pack собирает пакет коннектора из каталога исходников (Go) или готового модуля.
func (e *Env) pack(ctx context.Context, args []string) int {
	pos, fl := splitFlags(args, map[string]bool{"wasm": true, "key": true, "o": true, "platform": true})
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	wasmFile := fs.String("wasm", "", "ready connector.wasm (default: build the Go sources in <dir>)")
	keyFile := fs.String("key", "", "publisher private key file (gate4ai connectors keygen); without it the package is unsigned")
	out := fs.String("o", "", "output file (default <connector>-<version>.g4c; native: <connector>-<version>.platforms.json)")
	platform := fs.String("platform", "", "native: only these platforms (os/arch,…; default manifest.platforms)")
	if fs.Parse(fl) != nil || len(pos) != 1 {
		fmt.Fprintln(e.Stderr, connectorsUsage)
		return ExitUsage
	}
	dir := pos[0]
	man, err := os.ReadFile(filepath.Join(dir, store.ManifestFile))
	if err != nil {
		return e.fail(err)
	}
	var key ed25519.PrivateKey
	if *keyFile != "" {
		kb, err := os.ReadFile(*keyFile)
		if err != nil {
			return e.fail(err)
		}
		if key, err = registry.ParsePrivateKey(string(kb)); err != nil {
			return e.fail(err)
		}
	}
	m, err := manifest.Parse(man)
	if err != nil {
		return e.fail(err)
	}
	if m.Runtime == manifest.RuntimeNative {
		return e.packNative(ctx, dir, man, m, key, *platform, *out)
	}
	var wb []byte
	var toolchain string
	if *wasmFile != "" {
		wb, err = os.ReadFile(*wasmFile)
	} else {
		wb, toolchain, err = buildModule(ctx, dir)
	}
	if err != nil {
		return e.fail(err)
	}
	pkg, err := store.Pack(man, wb, key)
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
	res := map[string]any{"package": filepath.Base(*out), "connector": p.Manifest.Connector, "version": p.Manifest.Version,
		"digest": p.Sig.Digest, "sha256": store.SHA256(pkg), "signed_by": p.Sig.PublicKey, "capabilities": p.Manifest.Summary()}
	if toolchain != "" {
		res["go"] = toolchain
	}
	e.printJSON(res)
	return ExitOK
}

// packNative собирает нативный пакет под каждую платформу и список платформ.
func (e *Env) packNative(ctx context.Context, dir string, man []byte, m manifest.Manifest, key ed25519.PrivateKey, only, out string) int {
	plats := m.Platforms
	if only != "" {
		plats = strings.Split(only, ",")
	}
	tc, err := wasm.Toolchain(dir)
	if err != nil {
		return e.fail(err)
	}
	single := len(plats) == 1 && strings.HasSuffix(out, store.PackageExt)
	if out == "" {
		out = m.Connector + "-" + m.Version + store.SetExt
	}
	base := filepath.Dir(out)
	set := store.PlatformSet{Connector: m.Connector, Version: m.Version, Platforms: map[string]store.Artifact{}}
	var signedBy string
	for _, plat := range plats {
		bin, err := buildNative(ctx, dir, plat)
		if err != nil {
			return e.fail(err)
		}
		pkg, err := store.PackNative(man, bin, plat, key)
		if err != nil {
			return e.fail(err)
		}
		p, err := store.Open(pkg)
		if err != nil {
			return e.fail(err)
		}
		signedBy = p.Sig.PublicKey
		name := m.Connector + "-" + m.Version + "-" + strings.ReplaceAll(plat, "/", "-") + store.PackageExt
		file := filepath.Join(base, name)
		if single {
			file = out
		}
		if err := os.WriteFile(file, pkg, 0o644); err != nil {
			return e.fail(err)
		}
		if single {
			e.printJSON(map[string]any{"package": filepath.Base(file), "connector": m.Connector, "version": m.Version, "platform": plat,
				"digest": p.Sig.Digest, "sha256": store.SHA256(pkg), "signed_by": signedBy, "capabilities": m.Summary(), "go": tc})
			return ExitOK
		}
		set.Platforms[plat] = store.Artifact{URL: name, SHA256: store.SHA256(pkg), Digest: p.Sig.Digest}
	}
	sb, _ := json.MarshalIndent(set, "", "  ")
	sb = append(sb, '\n')
	if err := os.WriteFile(out, sb, 0o644); err != nil {
		return e.fail(err)
	}
	e.printJSON(map[string]any{"package": filepath.Base(out), "connector": m.Connector, "version": m.Version,
		"digest": store.SetDigest(set), "sha256": store.SHA256(sb), "signed_by": signedBy, "capabilities": m.Summary(),
		"go": tc, "platforms": set.Platforms})
	return ExitOK
}

// buildNative собирает бинарник нативного коннектора под платформу.
func buildNative(ctx context.Context, dir, platform string) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "g4apack")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, store.BinaryName(platform))
	if err := native.BuildGo(ctx, dir, platform, bin); err != nil {
		return nil, err
	}
	return os.ReadFile(bin)
}

// buildModule собирает WASM-модуль из исходников dir и возвращает его и версию Go.
func buildModule(ctx context.Context, dir string) ([]byte, string, error) {
	tc, err := wasm.Toolchain(dir)
	if err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp("", "g4apack")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, store.ModuleFile)
	if err := wasm.BuildGo(ctx, dir, bin); err != nil {
		return nil, "", err
	}
	wb, err := os.ReadFile(bin)
	return wb, tc, err
}

// verifyPackage пересобирает пакет из исходников и сравнивает с готовым: так ревьюер
// registry убеждается, что пакет собран из этого кода (подпись издателя этого
// не доказывает). Для нативного — каждый пакет из списка платформ.
func (e *Env) verifyPackage(ctx context.Context, args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(e.Stderr, connectorsUsage)
		return ExitUsage
	}
	dir, file := args[0], args[1]
	man, err := os.ReadFile(filepath.Join(dir, store.ManifestFile))
	if err != nil {
		return e.fail(err)
	}
	raw, err := store.Fetch(ctx, file)
	if err != nil {
		return e.fail(err)
	}
	type item struct {
		ref string
		raw []byte
	}
	items := []item{{file, raw}}
	var set *store.PlatformSet
	if store.IsSet(file) {
		s, err := store.ParseSet(raw)
		if err != nil {
			return e.fail(err)
		}
		set = &s
		items = nil
		for _, plat := range slices.Sorted(maps.Keys(s.Platforms)) {
			ref := store.ResolveRef(file, s.Platforms[plat].URL)
			b, err := store.Fetch(ctx, ref)
			if err != nil {
				return e.fail(err)
			}
			if store.SHA256(b) != s.Platforms[plat].SHA256 {
				return e.fail(fmt.Errorf("%s: sha256 does not match the platforms list", ref))
			}
			items = append(items, item{ref, b})
		}
	}
	var tc string
	var p *store.Package
	for _, it := range items {
		if p, err = store.Open(it.raw); err != nil {
			return e.fail(fmt.Errorf("%s: %w", it.ref, err))
		}
		var built []byte
		have := p.Wasm
		if p.Native() {
			have = p.Binary
			if set != nil && set.Platforms[p.Sig.Platform].Digest != p.Sig.Digest {
				return e.fail(fmt.Errorf("%s: digest does not match the platforms list", it.ref))
			}
			if tc, err = wasm.Toolchain(dir); err == nil {
				built, err = buildNative(ctx, dir, p.Sig.Platform)
			}
		} else {
			built, tc, err = buildModule(ctx, dir)
		}
		if err != nil {
			return e.fail(err)
		}
		var diff []string
		if !bytes.Equal(man, p.ManifestYAML) {
			diff = append(diff, store.ManifestFile)
		}
		if !bytes.Equal(built, have) {
			name := store.ModuleFile
			if p.Native() {
				name = store.BinaryName(p.Sig.Platform) + " (" + p.Sig.Platform + ")"
			}
			diff = append(diff, name)
		}
		if len(diff) > 0 {
			return e.fail(fmt.Errorf("%s does not match the sources in %s (built with %s): %s differs", it.ref, dir, tc, strings.Join(diff, ", ")))
		}
	}
	res := map[string]any{"status": "ok", "reproducible": true, "connector": p.Manifest.Connector, "version": p.Manifest.Version,
		"digest": p.Sig.Digest, "sha256": store.SHA256(raw), "signed_by": p.SignedBy(), "go": tc}
	if set != nil {
		res["digest"], res["platforms"] = store.SetDigest(*set), len(items)
	}
	e.printJSON(res)
	return ExitOK
}

const registryUsage = `usage:
  gate4ai registry check <index.json> [--key ed25519:…] [--fetch]   structure, signature; --fetch: download and verify every package
  gate4ai registry sign <index.json> --key file                      sign the index (registry owner)
  gate4ai registry add <index.json> <pack.json> --publisher name [--repo https://…] [--path dir] [--license id] [--description text] [--url package-url]`

func (e *Env) registry(ctx context.Context, args []string) int {
	if len(args) == 0 {
		args = []string{"help"}
	}
	sub := args[0]
	pos, fl := splitFlags(args[1:], map[string]bool{"key": true, "publisher": true, "repo": true, "path": true, "license": true, "description": true, "url": true})
	fs := flag.NewFlagSet("registry", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	key := fs.String("key", "", "check: registry public key (ed25519:…); sign: registry private key file")
	fetch := fs.Bool("fetch", false, "check: download every package and verify it against its entry")
	publisher := fs.String("publisher", "", "add: publisher name in the index")
	repo := fs.String("repo", "", "add: source repository (required for a new connector)")
	path := fs.String("path", "", "add: the connector's folder in --repo (new connector; default: the repo root)")
	license := fs.String("license", "", "add: SPDX license (required for a new connector)")
	description := fs.String("description", "", "add: short description (new connector)")
	pkgURL := fs.String("url", "", "add: package URL (default: GitHub release v<version> of --repo or of the existing entry)")
	want := 1
	if sub == "add" {
		want = 2
	}
	if fs.Parse(fl) != nil || len(pos) != want {
		fmt.Fprintln(e.Stderr, registryUsage)
		return ExitUsage
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return e.fail(err)
	}
	decode := func() (*registry.Index, error) {
		var ix registry.Index
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ix); err != nil {
			return nil, err
		}
		return &ix, ix.Check()
	}
	switch sub {
	case "check":
		ix, err := decode()
		if err != nil {
			return e.fail(err)
		}
		res := map[string]any{"status": "ok", "connectors": len(ix.Connectors), "publishers": len(ix.Publishers), "revoked": len(ix.Revoked)}
		if *key != "" {
			sig, err := os.ReadFile(pos[0] + ".sig")
			if err != nil {
				return e.fail(err)
			}
			if _, err := registry.ParseIndex(raw, strings.TrimSpace(string(sig)), []string{*key}); err != nil {
				return e.fail(err)
			}
			res["signature"] = "valid"
		}
		if *fetch {
			n, errs := store.VerifyPackages(ctx, ix, pos[0])
			if len(errs) > 0 {
				return e.fail(errors.New("packages: " + strings.Join(errs, "; ")))
			}
			res["packages_verified"] = n
		}
		e.printJSON(res)
		return ExitOK
	case "sign":
		kb, err := os.ReadFile(*key)
		if err != nil {
			return e.fail(err)
		}
		sig, err := registry.SignIndex(raw, string(kb))
		if err != nil {
			return e.fail(err)
		}
		if err := os.WriteFile(pos[0]+".sig", []byte(sig+"\n"), 0o644); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok", "signature": pos[0] + ".sig"})
		return ExitOK
	case "add":
		ix, err := decode()
		if err != nil {
			return e.fail(err)
		}
		pb, err := os.ReadFile(pos[1])
		if err != nil {
			return e.fail(err)
		}
		var p store.PackInfo
		if err := json.Unmarshal(pb, &p); err != nil {
			return e.fail(fmt.Errorf("%s: %w", pos[1], err))
		}
		u := *pkgURL
		if u == "" {
			r := *repo
			if old, ok := ix.Connectors[p.Connector]; ok && r == "" {
				r = old.Repo
			}
			if !strings.HasPrefix(r, "https://github.com/") {
				return e.fail(errors.New("--url is required unless the repository is on GitHub"))
			}
			file := p.Package
			if file == "" {
				file = p.Connector + "-" + p.Version + store.PackageExt
			}
			u = strings.TrimSuffix(r, "/") + "/releases/download/v" + p.Version + "/" + file
		}
		if err := store.AddRelease(ix, *publisher, p, u, registry.Entry{Repo: *repo, Path: *path, License: *license, Description: *description}); err != nil {
			return e.fail(err)
		}
		out, _ := json.MarshalIndent(ix, "", "  ")
		if err := os.WriteFile(pos[0], append(out, '\n'), 0o644); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok", "connector": p.Connector, "version": p.Version, "url": u,
			"publisher": *publisher, "trust": ix.Publishers[*publisher].Trust, "note": "the index signature is now stale: the registry owner re-signs after review"})
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, registryUsage)
	return ExitUsage
}
