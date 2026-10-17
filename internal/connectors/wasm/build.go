// SPDX-License-Identifier: AGPL-3.0-only

package wasm

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// BuildGo собирает Go-коннектор из srcDir в WASM-модуль out (нужен Go в PATH):
// GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -buildvcs=false
// тулчейном из go.mod (Toolchain). Сборка воспроизводима: модуль зависит только
// от исходников, go.sum и версии Go, но не от каталога, состояния git и Go на машине.
func BuildGo(ctx context.Context, srcDir, out string) error {
	tc, err := Toolchain(srcDir)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-buildmode=c-shared", "-trimpath", "-buildvcs=false", "-o", out, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOFLAGS=", "GOTOOLCHAIN="+tc)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %v\n%s", srcDir, err, b)
	}
	return nil
}

var goVersionRe = regexp.MustCompile(`^1\.\d+(\.\d+)?$`)

// Toolchain — версия Go для сборки коннектора из go.mod его модуля (srcDir
// или выше): директива toolchain, иначе go (1.27 → go1.27.0). Её же печатает
// pack, чтобы ревьюер пересобрал пакет тем же Go.
func Toolchain(srcDir string) (string, error) {
	dir, err := filepath.Abs(srcDir)
	if err != nil {
		return "", err
	}
	var f *os.File
	for {
		if f, err = os.Open(filepath.Join(dir, "go.mod")); err == nil || !os.IsNotExist(err) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s: go.mod not found", srcDir)
		}
		dir = parent
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	var goLine, tcLine string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) != 2 {
			continue
		}
		switch fs[0] {
		case "go":
			goLine = fs[1]
		case "toolchain":
			tcLine = fs[1]
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if tcLine != "" {
		if !goVersionRe.MatchString(strings.TrimPrefix(tcLine, "go")) {
			return "", fmt.Errorf("go.mod: unsupported toolchain %q", tcLine)
		}
		return tcLine, nil
	}
	if !goVersionRe.MatchString(goLine) {
		return "", fmt.Errorf("go.mod: no usable go version (%q)", goLine)
	}
	if strings.Count(goLine, ".") == 1 {
		goLine += ".0"
	}
	return "go" + goLine, nil
}
