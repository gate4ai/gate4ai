// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build !linux && !darwin && !windows

package cli

import "os"

// IsTerminal — f — символьное устройство (приближение без termios).
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
