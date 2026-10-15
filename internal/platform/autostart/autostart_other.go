// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build !linux && !darwin && !windows

package autostart

import (
	"errors"
	"runtime"
)

func Install(Spec) (Result, error) {
	return Result{}, errors.New("autostart is not supported on " + runtime.GOOS + ": run gate4ai daemon start from your login scripts")
}

func Uninstall(string, bool) ([]string, []string, error) { return nil, nil, nil }

func State(string) map[string]any { return map[string]any{"installed": false} }
