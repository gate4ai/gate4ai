// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build !linux && !darwin && !windows

package cli

import (
	"errors"
	"runtime"
)

func installService(serviceSpec) (serviceResult, error) {
	return serviceResult{}, errors.New("autostart is not supported on " + runtime.GOOS + ": run gate4ai daemon start from your login scripts")
}

func uninstallService(string) ([]string, []string, error) { return nil, nil, nil }

func serviceState(string) map[string]any { return map[string]any{"installed": false} }
