// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// PrepareDevWasm — своя сборка агента из папки (install_dev_connector):
// манифест читается один раз и должен совпасть с manifestSHA256 (тем, что
// одобрил человек); собирается только WASM. Пакет проходит обычный Prepare (доверие local), источник
// установки — сама папка (update пересоберёт из неё).
func (o Options) PrepareDevWasm(ctx context.Context, dir, manifestSHA256 string) (*Plan, error) {
	man, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	if SHA256(man) != manifestSHA256 {
		return nil, errors.New("manifest.yaml changed after the request was shown to the owner; ask again")
	}
	m, err := manifest.Parse(man)
	if err != nil {
		return nil, err
	}
	if m.Runtime != manifest.RuntimeWasm {
		return nil, fmt.Errorf("own builds of agents are WASM only (%s is %s)", m.Connector, m.Runtime)
	}
	tmp, err := os.MkdirTemp("", "g4adev")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, ModuleFile)
	if err := wasm.BuildGo(ctx, dir, out); err != nil {
		return nil, err
	}
	wb, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	b, err := Pack(man, wb, nil)
	if err != nil {
		return nil, err
	}
	pkgFile := filepath.Join(tmp, m.Connector+PackageExt)
	if err := os.WriteFile(pkgFile, b, 0o600); err != nil {
		return nil, err
	}
	p, err := o.Prepare(ctx, pkgFile, true)
	if err != nil {
		return nil, err
	}
	// Сверка по собранному пакету (а не по отдельному чтению манифеста): тот же
	// манифест, что показали человеку, только WASM.
	if SHA256(p.pkg.ManifestYAML) != manifestSHA256 || p.pkg.Manifest.Runtime != manifest.RuntimeWasm {
		return nil, errors.New("the built package does not match the approved manifest")
	}
	p.Source = SourceDir + dir
	return p, nil
}
