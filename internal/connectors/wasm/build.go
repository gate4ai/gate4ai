// SPDX-License-Identifier: FSL-1.1-ALv2

package wasm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// BuildGo собирает Go-коннектор из srcDir в WASM-модуль out (нужен Go в PATH):
// GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath.
func BuildGo(ctx context.Context, srcDir, out string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-buildmode=c-shared", "-trimpath", "-o", out, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %v\n%s", srcDir, err, b)
	}
	return nil
}
