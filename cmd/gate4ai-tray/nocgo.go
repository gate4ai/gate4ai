// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin && !cgo

// Команда gate4ai-tray — трей gate4.ai. На macOS трей собирается только с cgo
// (CGO_ENABLED=1 на macOS); эта заглушка держит кросс-сборку ./... без cgo.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "gate4ai-tray: built without cgo; on macOS build with CGO_ENABLED=1")
	os.Exit(1)
}
