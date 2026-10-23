// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin && !windows

package procinfo

import "errors"

func lookup(pid int, keys []string) (Proc, error) {
	return Proc{}, errors.New("procinfo: not supported on this OS")
}

func cwdOf(pid int) string { return "" }
