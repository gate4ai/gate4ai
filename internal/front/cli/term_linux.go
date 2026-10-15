// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal — f — терминал (не файл, не pipe, не /dev/null).
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}
