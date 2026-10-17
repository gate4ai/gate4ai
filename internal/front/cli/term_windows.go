// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// IsTerminal — f — консоль (не файл и не pipe).
func IsTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
