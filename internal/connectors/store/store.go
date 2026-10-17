// SPDX-License-Identifier: AGPL-3.0-only

// Package store — магазин коннекторов: пакет (.g4c) с подписью издателя,
// индекс registry с подписью, установка с согласием владельца на возможности,
// отзыв версий и загрузка установленных коннекторов в WASM-хост. Каталог
// установленного коннектора: <home>/connectors/<имя>/{manifest.yaml,
// connector.wasm, install.json}.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/gate4ai/sdk-go/abi"

	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
)

// Имена файлов установленного пакета.
const (
	ManifestFile = "manifest.yaml"
	ModuleFile   = "connector.wasm"
)

// Installed — установленный коннектор.
type Installed struct {
	Name     string
	Dir      string
	Manifest manifest.Manifest
	Info     Info
}

// Module — путь к WASM-модулю.
func (i Installed) Module() string { return filepath.Join(i.Dir, ModuleFile) }

// List — установленные коннекторы (каталоги с манифестом и модулем).
func List(dir string) ([]Installed, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, e := range ents {
		if !e.IsDir() || e.Name() == "data" {
			continue
		}
		d := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(filepath.Join(d, ManifestFile))
		if err != nil {
			continue
		}
		m, err := manifest.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		if m.Connector != e.Name() {
			return nil, fmt.Errorf("%s: manifest declares connector %q", d, m.Connector)
		}
		it := Installed{Name: m.Connector, Dir: d, Manifest: m}
		if ib, err := os.ReadFile(filepath.Join(d, InfoFile)); err == nil {
			_ = json.Unmarshal(ib, &it.Info)
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LoadAll загружает установленные коннекторы в WASM-хост: манифест пакета —
// сразу, компиляция — в фоне (первый вызов её дожидается). Загружается только
// установленное через Install (install.json с согласием), с целыми файлами
// (дайджест) и не отозванное (кэш индекса registry). Коннектор с ошибкой
// пропускается (ошибка — в лог), остальные работают.
func (o Options) LoadAll(ctx context.Context, e *wasm.Engine, log *slog.Logger) []connectors.Client {
	list, err := List(o.Dir)
	if err != nil {
		log.Error("connectors", "err", err)
		return nil
	}
	ix := o.cachedIndex()
	var out []connectors.Client
	for _, it := range list {
		c, err := o.load(it, ix, e)
		if err != nil {
			log.Error("connector not loaded", "connector", it.Name, "err", err)
			continue
		}
		out = append(out, c)
	}
	return out
}

// Load загружает один установленный коннектор (после установки или обновления).
func (o Options) Load(name string, e *wasm.Engine) (connectors.Client, error) {
	list, err := List(o.Dir)
	if err != nil {
		return nil, err
	}
	for _, it := range list {
		if it.Name == name {
			return o.load(it, o.cachedIndex(), e)
		}
	}
	return nil, fmt.Errorf("connector %s is not installed", name)
}

// ErrRevoked — версия коннектора отозвана.
var ErrRevoked = errors.New("revoked")

// Check проверяет установленный коннектор без загрузки (целостность, согласие, отзыв).
func (o Options) Check(name string) error {
	list, err := List(o.Dir)
	if err != nil {
		return err
	}
	for _, it := range list {
		if it.Name == name {
			_, err := o.check(it, o.cachedIndex())
			return err
		}
	}
	return fmt.Errorf("connector %s is not installed", name)
}

func (o Options) load(it Installed, ix *Index, e *wasm.Engine) (connectors.Client, error) {
	bin, err := o.check(it, ix)
	if err != nil {
		return nil, err
	}
	var c connectors.Client = e.LoadLazy(bin, it.Manifest)
	if it.Info.Trust == TrustCommunity {
		c = readOnly(c)
	}
	return c, nil
}

func (o Options) check(it Installed, ix *Index) ([]byte, error) {
	if it.Info.Connector != it.Name {
		return nil, errors.New("not installed with `gate4ai connectors install` (no install.json)")
	}
	if it.Manifest.Runtime != manifest.RuntimeWasm {
		return nil, fmt.Errorf("only runtime wasm is supported, got %q", it.Manifest.Runtime)
	}
	mb, err := os.ReadFile(filepath.Join(it.Dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	bin, err := os.ReadFile(it.Module())
	if err != nil {
		return nil, err
	}
	if Digest(mb, bin) != it.Info.Digest {
		return nil, errors.New("files changed after installation (digest mismatch)")
	}
	if !equalStrings(it.Manifest.Capabilities.Summary(), nonNil(it.Info.Capabilities)) {
		return nil, errors.New("capabilities differ from what the owner consented to")
	}
	if ix != nil {
		if rev, why := ix.IsRevoked(it.Name, it.Manifest.Version); rev {
			return nil, fmt.Errorf("version %s is %w: %s", it.Manifest.Version, ErrRevoked, why)
		}
	}
	return bin, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// readOnlyClient — коннектор сообщества: доступны только действия класса read.
type readOnlyClient struct {
	connectors.Client
	man manifest.Manifest
}

func readOnly(c connectors.Client) connectors.Client {
	m := c.Manifest()
	var acts []manifest.Action
	for _, a := range m.Actions {
		if a.Class == "read" {
			acts = append(acts, a)
		}
	}
	m.Actions = acts
	return &readOnlyClient{Client: c, man: m}
}

func (r *readOnlyClient) Manifest() manifest.Manifest { return r.man }

func (r *readOnlyClient) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	if _, ok := r.man.Find(req.Action); !ok {
		return abi.ExecResult{}, fmt.Errorf("action %s is not available for a community connector (read only)", req.Action)
	}
	return r.Client.Execute(ctx, req)
}
