// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os/exec"
	"runtime"
)

// OpenURL открывает ссылку в браузере (подменяется в тестах). Процесс
// открывателя реапится, чтобы долгоживущий трей не копил зомби.
var OpenURL = func(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
